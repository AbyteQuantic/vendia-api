package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// setupSupportDB opens an in-memory SQLite DB with the narrow schema
// the support endpoints touch. Sidesteps the Tenant/User models'
// Postgres-specific defaults (gen_random_uuid, jsonb) by hand-crafting
// the tables — only the columns this feature reads are present.
func setupSupportDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	stmts := []string{
		`CREATE TABLE tenants (
			id TEXT PRIMARY KEY, deleted_at DATETIME,
			business_name TEXT NOT NULL DEFAULT '',
			phone TEXT NOT NULL DEFAULT '',
			created_at DATETIME)`,
		`CREATE TABLE users (
			id TEXT PRIMARY KEY, deleted_at DATETIME,
			phone TEXT NOT NULL DEFAULT '',
			created_at DATETIME)`,
		`CREATE TABLE support_tickets (
			id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL,
			user_id TEXT,
			subject TEXT NOT NULL,
			message TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'OPEN',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL)`,
	}
	for _, s := range stmts {
		require.NoError(t, db.Exec(s).Error)
	}
	return db
}

func mountTenantSupport(db *gorm.DB, tenantID, userID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if tenantID != "" {
			c.Set(middleware.TenantIDKey, tenantID)
		}
		if userID != "" {
			c.Set(middleware.UserIDKey, userID)
		}
		c.Next()
	})
	r.POST("/api/v1/support", handlers.CreateSupportTicket(db))
	return r
}

func postJSONSupport(t *testing.T, r *gin.Engine, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/support", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCreateSupportTicket_PersistsTicketWithOpenStatus(t *testing.T) {
	db := setupSupportDB(t)
	r := mountTenantSupport(db, "tenant-1", "user-1")

	w := postJSONSupport(t, r, map[string]any{
		"subject": "No me sincroniza el catálogo",
		"message": "El botón queda girando y nada más.",
	})
	require.Equal(t, http.StatusCreated, w.Code)

	var body struct {
		Data models.SupportTicket `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "tenant-1", body.Data.TenantID)
	require.NotNil(t, body.Data.UserID)
	assert.Equal(t, "user-1", *body.Data.UserID)
	assert.Equal(t, models.TicketStatusOpen, body.Data.Status)
	assert.Equal(t, "No me sincroniza el catálogo", body.Data.Subject)
}

func TestCreateSupportTicket_WhitespaceOnlySubjectRejected(t *testing.T) {
	db := setupSupportDB(t)
	r := mountTenantSupport(db, "tenant-1", "user-1")

	w := postJSONSupport(t, r, map[string]any{
		"subject": "   ",
		"message": "Mensaje válido",
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid_request")
}

func TestCreateSupportTicket_LongSubjectIsTruncated(t *testing.T) {
	db := setupSupportDB(t)
	r := mountTenantSupport(db, "tenant-1", "user-1")

	subject := strings.Repeat("A", 200) // 200 chars, column is 160
	w := postJSONSupport(t, r, map[string]any{
		"subject": subject,
		"message": "cuerpo",
	})
	require.Equal(t, http.StatusCreated, w.Code)

	var body struct {
		Data models.SupportTicket `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Len(t, body.Data.Subject, 160)
}

func TestCreateSupportTicket_RejectsWhenNoTenantID(t *testing.T) {
	db := setupSupportDB(t)
	r := mountTenantSupport(db, "", "") // no auth context

	w := postJSONSupport(t, r, map[string]any{"subject": "x", "message": "y"})
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAdminListSupportTickets_OrdersOpenFirstThenNewest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSupportDB(t)
	now := time.Now().UTC()

	require.NoError(t, db.Exec(`
		INSERT INTO tenants (id, business_name, phone, created_at) VALUES
			('t1','Tienda Pedro','3001111111',?),
			('t2','Restaurante Ana','3002222222',?)
	`, now, now).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO users (id, phone, created_at) VALUES
			('u1','3001111111',?), ('u2','3002222222',?)
	`, now, now).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO support_tickets (id, tenant_id, user_id, subject, message, status, created_at, updated_at) VALUES
			('k1','t1','u1','viejo resuelto','msg','RESOLVED',?,?),
			('k2','t1','u1','reciente resuelto','msg','RESOLVED',?,?),
			('k3','t2','u2','abierto viejo','msg','OPEN',?,?),
			('k4','t2','u2','abierto nuevo','msg','OPEN',?,?)
	`,
		now.Add(-48*time.Hour), now.Add(-48*time.Hour),
		now.Add(-1*time.Hour), now.Add(-1*time.Hour),
		now.Add(-24*time.Hour), now.Add(-24*time.Hour),
		now.Add(-30*time.Minute), now.Add(-30*time.Minute),
	).Error)

	r := gin.New()
	r.GET("/admin/support/tickets", handlers.AdminListSupportTickets(db))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/admin/support/tickets", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Data []handlers.AdminTicketRow `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Data, 4)

	// Expected order: open-newest, open-oldest, resolved-newest, resolved-oldest
	assert.Equal(t, "abierto nuevo", body.Data[0].Subject)
	assert.Equal(t, "abierto viejo", body.Data[1].Subject)
	assert.Equal(t, "reciente resuelto", body.Data[2].Subject)
	assert.Equal(t, "viejo resuelto", body.Data[3].Subject)

	// Joined context is present on every row.
	assert.Equal(t, "Restaurante Ana", body.Data[0].BusinessName)
	assert.Equal(t, "3002222222", body.Data[0].TenantPhone)
	assert.Equal(t, "3002222222", body.Data[0].UserPhone)
}

func TestAdminUpdateSupportTicket_FlipsToResolved(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSupportDB(t)
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO tenants (id, business_name, phone, created_at) VALUES
			('t1','T1','3001111111',?)
	`, now).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO support_tickets (id, tenant_id, subject, message, status, created_at, updated_at)
		VALUES ('kid','t1','sub','msg','OPEN',?,?)
	`, now, now).Error)

	r := gin.New()
	r.PATCH("/admin/support/tickets/:id", handlers.AdminUpdateSupportTicket(db))

	w := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]string{"status": "RESOLVED"})
	req, _ := http.NewRequest(http.MethodPatch,
		"/admin/support/tickets/kid", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var ticket models.SupportTicket
	require.NoError(t, db.Where("id = ?", "kid").First(&ticket).Error)
	assert.Equal(t, "RESOLVED", ticket.Status)
}

func TestAdminUpdateSupportTicket_RejectsInvalidStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSupportDB(t)

	r := gin.New()
	r.PATCH("/admin/support/tickets/:id", handlers.AdminUpdateSupportTicket(db))

	w := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]string{"status": "ARCHIVED"})
	req, _ := http.NewRequest(http.MethodPatch,
		"/admin/support/tickets/any", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid_status")
}

func TestAdminUpdateSupportTicket_NotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSupportDB(t)

	r := gin.New()
	r.PATCH("/admin/support/tickets/:id", handlers.AdminUpdateSupportTicket(db))

	w := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]string{"status": "RESOLVED"})
	req, _ := http.NewRequest(http.MethodPatch,
		"/admin/support/tickets/does-not-exist", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestSortAdminTicketRowsOpenFirst_StableOrdering(t *testing.T) {
	rows := []handlers.AdminTicketRow{
		{ID: "1", Status: "RESOLVED", CreatedAt: "2026-04-18T10:00:00Z"},
		{ID: "2", Status: "OPEN", CreatedAt: "2026-04-19T10:00:00Z"},
		{ID: "3", Status: "RESOLVED", CreatedAt: "2026-04-20T10:00:00Z"},
		{ID: "4", Status: "OPEN", CreatedAt: "2026-04-21T10:00:00Z"},
	}
	sorted := handlers.SortAdminTicketRowsOpenFirst(rows)
	require.Len(t, sorted, 4)
	// OPENs first (newest → oldest), then RESOLVEDs (newest → oldest)
	assert.Equal(t, "4", sorted[0].ID)
	assert.Equal(t, "2", sorted[1].ID)
	assert.Equal(t, "3", sorted[2].ID)
	assert.Equal(t, "1", sorted[3].ID)
}
