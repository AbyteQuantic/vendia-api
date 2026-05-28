// Spec: specs/038-push-notifications-web-android/spec.md
package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/auth"
	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services/push"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupAdminPushDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.DeviceToken{},
		&models.Tenant{},
		&models.TenantSubscription{},
	))
	// Notifications a mano (gen_random_uuid Postgres-only).
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS notifications (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			tenant_id TEXT NOT NULL,
			title TEXT NOT NULL,
			body TEXT DEFAULT '',
			type TEXT DEFAULT 'info',
			is_read INTEGER DEFAULT 0,
			deep_link TEXT,
			pushed_at DATETIME,
			dedup_key TEXT
		)
	`).Error)
	return db
}

// adminPushRouter inyecta claims controlables (super_admin true/false)
// para simular el JWT del operador de VendIA.
func adminPushRouter(db *gorm.DB, dispatcher *push.Dispatcher, isSuperAdmin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	inject := func(c *gin.Context) {
		c.Set(middleware.ClaimsKey, &auth.Claims{
			TenantID:     "admin-operator-tenant",
			IsSuperAdmin: isSuperAdmin,
		})
		c.Next()
	}
	g := r.Group("/api/v1/admin", inject, middleware.SuperAdminOnly())
	g.POST("/push/broadcast", handlers.BroadcastPush(db, dispatcher))
	return r
}

func seedTenantWithToken(t *testing.T, db *gorm.DB, tenantID, status string) {
	t.Helper()
	require.NoError(t, db.Create(&models.Tenant{
		BaseModel:    models.BaseModel{ID: tenantID},
		BusinessName: "Test",
		Phone:        "phone-" + tenantID[:8],
	}).Error)
	future := time.Now().Add(7 * 24 * time.Hour)
	sub := &models.TenantSubscription{TenantID: tenantID, Status: status}
	if status == models.SubscriptionStatusTrial {
		sub.TrialEndsAt = &future
	}
	require.NoError(t, db.Create(sub).Error)
	require.NoError(t, db.Create(&models.DeviceToken{
		TenantID: tenantID, UserID: "user-x",
		Token: "tok-" + tenantID[:6], Platform: "web", LastSeenAt: time.Now(),
	}).Error)
}

func postAdminBroadcast(r *gin.Engine, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/push/broadcast", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// AC-09 — un super_admin envía broadcast a un tenant válido; el
// dispatcher recibe el evento con tipo "admin_manual" y los tokens
// del tenant reciben push.
func TestBroadcastPush_SuperAdminSendsToTenant(t *testing.T) {
	db := setupAdminPushDB(t)
	fake := &push.FakeSender{}
	dispatcher := push.NewDispatcher(fake)

	tenantID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	seedTenantWithToken(t, db, tenantID, models.SubscriptionStatusTrial)

	r := adminPushRouter(db, dispatcher, true)
	w := postAdminBroadcast(r, map[string]any{
		"tenant_id": tenantID,
		"title":     "Mantenimiento programado",
		"body":      "Estaremos haciendo mantenimiento mañana de 2 a 4 a.m.",
		"deep_link": "/avisos",
	})
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

	require.Len(t, fake.Calls, 1)
	assert.Equal(t, "Mantenimiento programado", fake.Calls[0].Payload.Title)
	assert.Equal(t, "/avisos", fake.Calls[0].Payload.DeepLink)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.NotEmpty(t, data["in_app_notification_id"])
	assert.EqualValues(t, 1, data["tokens_targeted"])
}

// Un user que NO es super_admin recibe 403 — defensa Art. VI.
func TestBroadcastPush_NonSuperAdminRejected(t *testing.T) {
	db := setupAdminPushDB(t)
	dispatcher := push.NewDispatcher(&push.FakeSender{})

	r := adminPushRouter(db, dispatcher, false) // ← no super_admin
	w := postAdminBroadcast(r, map[string]any{
		"tenant_id": "any", "title": "x", "body": "y",
	})
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

// AC-16 — tenant suspendido (PRO_PAST_DUE) NO recibe push del admin
// (canal cerrado completamente, sin excepción para "cobranza").
func TestBroadcastPush_SuspendedTenantNotSent(t *testing.T) {
	db := setupAdminPushDB(t)
	fake := &push.FakeSender{}
	dispatcher := push.NewDispatcher(fake)

	tenantID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	seedTenantWithToken(t, db, tenantID, models.SubscriptionStatusProPastDue)

	r := adminPushRouter(db, dispatcher, true)
	w := postAdminBroadcast(r, map[string]any{
		"tenant_id": tenantID, "title": "Cobranza", "body": "Su factura está vencida",
	})
	// El endpoint responde 202 (recibido) pero NO se envió.
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Empty(t, fake.Calls, "tenant suspendido NO recibe push, ni del admin")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.EqualValues(t, 0, data["tokens_targeted"])
	assert.Equal(t, "skipped_suspended", data["status"])
}

// Tenant inexistente → 404 (Art. VI: no filtra existencia de tenants
// ajenos; basta con "no encontrado").
func TestBroadcastPush_NonExistentTenantReturns404(t *testing.T) {
	db := setupAdminPushDB(t)
	dispatcher := push.NewDispatcher(&push.FakeSender{})
	r := adminPushRouter(db, dispatcher, true)
	w := postAdminBroadcast(r, map[string]any{
		"tenant_id": "no-such-tenant", "title": "x", "body": "y",
	})
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

func TestBroadcastPush_ValidatesBody(t *testing.T) {
	db := setupAdminPushDB(t)
	dispatcher := push.NewDispatcher(&push.FakeSender{})
	r := adminPushRouter(db, dispatcher, true)

	// Sin tenant_id.
	w := postAdminBroadcast(r, map[string]any{"title": "x", "body": "y"})
	assert.Equal(t, http.StatusBadRequest, w.Code)
	// Sin title.
	w2 := postAdminBroadcast(r, map[string]any{"tenant_id": "t", "body": "y"})
	assert.Equal(t, http.StatusBadRequest, w2.Code)
}
