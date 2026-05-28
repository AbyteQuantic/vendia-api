// Spec: specs/038-push-notifications-web-android/spec.md
package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupDevicesDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.DeviceToken{}))
	return db
}

// devicesRouter monta los 3 endpoints con un middleware fake que
// inyecta tenant_id + user_id en el contexto, simulando lo que el
// JWT middleware haría en producción. Si el caller no pasa user_id
// (token sin claim), el inject deja el contexto vacío para simular
// el caso "JWT inválido".
func devicesRouter(db *gorm.DB, tenantID, userID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	inject := func(c *gin.Context) {
		if tenantID != "" {
			c.Set(middleware.TenantIDKey, tenantID)
		}
		if userID != "" {
			c.Set(middleware.UserIDKey, userID)
		}
		c.Next()
	}
	g := r.Group("/api/v1", inject)
	g.POST("/devices/register", handlers.RegisterDevice(db))
	g.GET("/devices/me", handlers.ListMyDevices(db))
	g.DELETE("/devices/me/:id", handlers.RevokeMyDevice(db))
	return r
}

func postDeviceJSON(r *gin.Engine, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// ─── REGISTER ────────────────────────────────────────────────────────

// AC-02 / FR-06 — primer registro crea la fila con tenant + user
// derivados del JWT (no del body), platform respetada, last_seen_at
// poblado.
func TestRegisterDevice_FirstRegistrationCreatesRow(t *testing.T) {
	db := setupDevicesDB(t)
	tenantID := "11111111-1111-1111-1111-111111111111"
	userID := "22222222-2222-2222-2222-222222222222"
	r := devicesRouter(db, tenantID, userID)

	label := "iPhone Safari"
	w := postDeviceJSON(r, "/api/v1/devices/register", map[string]any{
		"token":        "fcm:abc123",
		"platform":     "web",
		"device_label": label,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.NotEmpty(t, data["id"])
	assert.Equal(t, "web", data["platform"])

	var rows []models.DeviceToken
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, tenantID, rows[0].TenantID, "tenant viene del JWT, no del body")
	assert.Equal(t, userID, rows[0].UserID)
	assert.Equal(t, "fcm:abc123", rows[0].Token)
	assert.Equal(t, label, *rows[0].DeviceLabel)
	assert.Nil(t, rows[0].InvalidatedAt)
}

// Idempotencia (spec § 4): mismo token registrado dos veces NO
// duplica fila; el segundo POST refresca `last_seen_at` y retorna
// el mismo `id`.
func TestRegisterDevice_IsIdempotentRefreshesLastSeen(t *testing.T) {
	db := setupDevicesDB(t)
	tenantID := "11111111-1111-1111-1111-111111111111"
	userID := "22222222-2222-2222-2222-222222222222"
	r := devicesRouter(db, tenantID, userID)

	body := map[string]any{"token": "fcm:dup", "platform": "android"}

	w1 := postDeviceJSON(r, "/api/v1/devices/register", body)
	require.Equal(t, http.StatusCreated, w1.Code, w1.Body.String())
	var r1 map[string]any
	json.Unmarshal(w1.Body.Bytes(), &r1)
	id1 := r1["data"].(map[string]any)["id"].(string)

	// Sleep para asegurar que last_seen_at avanza.
	time.Sleep(10 * time.Millisecond)

	w2 := postDeviceJSON(r, "/api/v1/devices/register", body)
	require.Equal(t, http.StatusCreated, w2.Code, w2.Body.String())
	var r2 map[string]any
	json.Unmarshal(w2.Body.Bytes(), &r2)
	id2 := r2["data"].(map[string]any)["id"].(string)

	assert.Equal(t, id1, id2, "idempotente: mismo id en segunda llamada")

	var count int64
	db.Model(&models.DeviceToken{}).Count(&count)
	assert.EqualValues(t, 1, count, "una sola fila persistida")

	var row models.DeviceToken
	require.NoError(t, db.First(&row, "token = ?", "fcm:dup").Error)
	assert.True(t, time.Since(row.LastSeenAt) < time.Second, "last_seen_at actualizado")
}

// Regla "un token nunca cambia de tenant" (spec § 7 invariantes):
// si el mismo token llega bajo otro tenant, la fila vieja se invalida
// y se crea una NUEVA bajo el tenant nuevo.
func TestRegisterDevice_RebindsTokenToNewTenant(t *testing.T) {
	db := setupDevicesDB(t)
	tokenStr := "fcm:rebindme"

	// Primer tenant registra el token.
	tenantA := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	userA := "aaaaaaaa-1111-aaaa-aaaa-aaaaaaaaaaaa"
	rA := devicesRouter(db, tenantA, userA)
	wA := postDeviceJSON(rA, "/api/v1/devices/register", map[string]any{
		"token": tokenStr, "platform": "web",
	})
	require.Equal(t, http.StatusCreated, wA.Code)

	// Otro tenant registra el MISMO token (mismo dispositivo, usuario
	// rotó de empleo).
	tenantB := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	userB := "bbbbbbbb-1111-bbbb-bbbb-bbbbbbbbbbbb"
	rB := devicesRouter(db, tenantB, userB)
	wB := postDeviceJSON(rB, "/api/v1/devices/register", map[string]any{
		"token": tokenStr, "platform": "web",
	})
	require.Equal(t, http.StatusCreated, wB.Code, wB.Body.String())

	// Resultado: 2 filas, la del tenant A invalidada, la del tenant B
	// activa.
	var allRows []models.DeviceToken
	require.NoError(t, db.Unscoped().Find(&allRows).Error)
	require.Len(t, allRows, 2)

	byTenant := map[string]models.DeviceToken{}
	for _, r := range allRows {
		byTenant[r.TenantID] = r
	}
	rowA := byTenant[tenantA]
	rowB := byTenant[tenantB]
	require.NotNil(t, rowA.InvalidatedAt, "fila del tenant A debe quedar invalidada")
	assert.Nil(t, rowB.InvalidatedAt, "fila del tenant B debe estar activa")
}

func TestRegisterDevice_RejectsInvalidPlatform(t *testing.T) {
	db := setupDevicesDB(t)
	r := devicesRouter(db, "tenant", "user")
	w := postDeviceJSON(r, "/api/v1/devices/register", map[string]any{
		"token": "fcm:x", "platform": "ios",
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestRegisterDevice_RejectsEmptyToken(t *testing.T) {
	db := setupDevicesDB(t)
	r := devicesRouter(db, "tenant", "user")
	w := postDeviceJSON(r, "/api/v1/devices/register", map[string]any{
		"token": "", "platform": "web",
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestRegisterDevice_RequiresJWT(t *testing.T) {
	db := setupDevicesDB(t)
	// Sin tenantID + sin userID — simulamos JWT ausente.
	r := devicesRouter(db, "", "")
	w := postDeviceJSON(r, "/api/v1/devices/register", map[string]any{
		"token": "fcm:x", "platform": "web",
	})
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}

// ─── LIST (GET /devices/me) ──────────────────────────────────────────

func TestListMyDevices_ReturnsOnlyMyActiveTokens(t *testing.T) {
	db := setupDevicesDB(t)
	tenantID := "33333333-3333-3333-3333-333333333333"
	me := "44444444-4444-4444-4444-444444444444"
	other := "55555555-5555-5555-5555-555555555555"

	now := time.Now()
	past := now.Add(-time.Hour)
	// Mías activas (2):
	require.NoError(t, db.Create(&models.DeviceToken{
		TenantID: tenantID, UserID: me, Token: "tok-mine-1", Platform: "web", LastSeenAt: now,
	}).Error)
	require.NoError(t, db.Create(&models.DeviceToken{
		TenantID: tenantID, UserID: me, Token: "tok-mine-2", Platform: "android", LastSeenAt: now,
	}).Error)
	// Mía invalidada (1):
	require.NoError(t, db.Create(&models.DeviceToken{
		TenantID: tenantID, UserID: me, Token: "tok-mine-dead", Platform: "web",
		LastSeenAt: now, InvalidatedAt: &past,
	}).Error)
	// De otro user del mismo tenant:
	require.NoError(t, db.Create(&models.DeviceToken{
		TenantID: tenantID, UserID: other, Token: "tok-other", Platform: "web", LastSeenAt: now,
	}).Error)

	r := devicesRouter(db, tenantID, me)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/devices/me", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].([]any)
	assert.Len(t, data, 2, "solo las 2 activas del usuario logueado")
}

func TestListMyDevices_RequiresJWT(t *testing.T) {
	db := setupDevicesDB(t)
	r := devicesRouter(db, "", "")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/devices/me", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ─── REVOKE (DELETE /devices/me/:id) ─────────────────────────────────

// AC-12 — el dueño puede borrar su propio token; queda con
// `invalidated_at` (soft-revoke para trazabilidad) y deja de aparecer
// en futuras consultas activas.
func TestRevokeMyDevice_SoftInvalidatesOwn(t *testing.T) {
	db := setupDevicesDB(t)
	tenantID := "66666666-6666-6666-6666-666666666666"
	me := "77777777-7777-7777-7777-777777777777"

	tok := models.DeviceToken{
		TenantID: tenantID, UserID: me, Token: "mine", Platform: "web", LastSeenAt: time.Now(),
	}
	require.NoError(t, db.Create(&tok).Error)

	r := devicesRouter(db, tenantID, me)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/devices/me/"+tok.ID, nil))
	assert.Equal(t, http.StatusNoContent, w.Code, w.Body.String())

	var reloaded models.DeviceToken
	require.NoError(t, db.First(&reloaded, "id = ?", tok.ID).Error)
	require.NotNil(t, reloaded.InvalidatedAt, "soft-revoke deja invalidated_at")
}

// El revoke NO permite borrar un token de OTRO usuario del mismo
// tenant — devuelve 404 (no 403 para no filtrar existencia).
func TestRevokeMyDevice_AnotherUserOfSameTenantReturns404(t *testing.T) {
	db := setupDevicesDB(t)
	tenantID := "66666666-6666-6666-6666-666666666666"
	me := "77777777-7777-7777-7777-777777777777"
	other := "88888888-8888-8888-8888-888888888888"

	tok := models.DeviceToken{
		TenantID: tenantID, UserID: other, Token: "theirs", Platform: "web", LastSeenAt: time.Now(),
	}
	require.NoError(t, db.Create(&tok).Error)

	r := devicesRouter(db, tenantID, me)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/devices/me/"+tok.ID, nil))
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())

	// El token sigue activo.
	var reloaded models.DeviceToken
	require.NoError(t, db.First(&reloaded, "id = ?", tok.ID).Error)
	assert.Nil(t, reloaded.InvalidatedAt)
}

// Tampoco se puede borrar un token de otro tenant (defensa Art. III).
func TestRevokeMyDevice_CrossTenantReturns404(t *testing.T) {
	db := setupDevicesDB(t)

	other := models.DeviceToken{
		TenantID: "tenant-other", UserID: "user-x",
		Token: "remote", Platform: "web", LastSeenAt: time.Now(),
	}
	require.NoError(t, db.Create(&other).Error)

	r := devicesRouter(db, "tenant-mine", "user-mine")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/devices/me/"+other.ID, nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRevokeMyDevice_RequiresJWT(t *testing.T) {
	db := setupDevicesDB(t)
	r := devicesRouter(db, "", "")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/devices/me/some-id", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
