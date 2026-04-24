package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupTableSessionsDB keeps the scope tight: only the tables we
// actually touch. Notifications is included so CallWaiter's
// side-effect doesn't blow up on a missing relation.
func setupTableSessionsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Tenant{},
		&models.OrderTicket{},
		&models.OrderItem{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Notifications uses a Postgres-specific `gen_random_uuid()`
	// default that SQLite can't parse. Rather than mutate the
	// production model, we stand up an equivalent table by hand —
	// the id is filled in by the app (not the DB) in this test.
	if err := db.Exec(`
		CREATE TABLE IF NOT EXISTS notifications (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			tenant_id TEXT NOT NULL,
			title TEXT NOT NULL,
			body TEXT DEFAULT '',
			type TEXT DEFAULT 'info',
			is_read INTEGER DEFAULT 0
		)
	`).Error; err != nil {
		t.Fatalf("migrate notifications: %v", err)
	}
	return db
}

func seedOpenOrder(t *testing.T, db *gorm.DB, tenantID, label string) models.OrderTicket {
	t.Helper()
	order := models.OrderTicket{
		BaseModel: models.BaseModel{ID: uuid.NewString()},
		TenantID:  tenantID,
		Label:     label,
		Status:    models.OrderStatusNuevo,
		Type:      models.OrderTypeMesa,
		Total:     25_000,
		Items: []models.OrderItem{
			{
				BaseModel:   models.BaseModel{ID: uuid.NewString()},
				ProductUUID: uuid.NewString(),
				ProductName: "Empanada",
				Quantity:    2,
				UnitPrice:   5_000,
				Emoji:       "🥟",
			},
			{
				BaseModel:   models.BaseModel{ID: uuid.NewString()},
				ProductUUID: uuid.NewString(),
				ProductName: "Coca-Cola",
				Quantity:    3,
				UnitPrice:   5_000,
				Emoji:       "🥤",
			},
		},
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return order
}

func getJSON(r http.Handler, path string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestOrderTicket_BeforeCreate_GeneratesSessionToken(t *testing.T) {
	db := setupTableSessionsDB(t)
	seedTenant(t, db, uuid.NewString(), "t1")

	order := seedOpenOrder(t, db, "", "Mesa 1") // re-fetch below

	// Reload because BeforeCreate mutates in-place but we want to
	// verify the persisted row, not the struct we handed to GORM.
	var got models.OrderTicket
	if err := db.First(&got, "id = ?", order.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.SessionToken == "" {
		t.Fatalf("expected session_token to be auto-generated")
	}
	if _, err := uuid.Parse(got.SessionToken); err != nil {
		t.Fatalf("session_token not a valid UUID: %q (%v)", got.SessionToken, err)
	}

	// Uniqueness: two orders for the same label must produce
	// different tokens. Protects against a regression that e.g.
	// seeds the field from a deterministic hash.
	second := seedOpenOrder(t, db, "", "Mesa 1")
	var got2 models.OrderTicket
	if err := db.First(&got2, "id = ?", second.ID).Error; err != nil {
		t.Fatalf("reload 2: %v", err)
	}
	if got2.SessionToken == got.SessionToken {
		t.Fatalf("session tokens collided: %s", got.SessionToken)
	}
}

func TestGetPublicTableSession_ReturnsProjectedShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableSessionsDB(t)
	tenant := seedTenant(t, db, uuid.NewString(), "brasas")

	order := seedOpenOrder(t, db, tenant.ID, "Mesa 7")
	var fresh models.OrderTicket
	db.First(&fresh, "id = ?", order.ID)

	r := gin.New()
	r.GET("/api/v1/public/table-sessions/:session_token",
		GetPublicTableSession(db))

	w := getJSON(r, "/api/v1/public/table-sessions/"+fresh.SessionToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.Data["table_label"] != "Mesa 7" {
		t.Fatalf("table_label mismatch: %v", body.Data["table_label"])
	}
	if body.Data["total"].(float64) != 25_000 {
		t.Fatalf("total mismatch: %v", body.Data["total"])
	}
	items, ok := body.Data["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("expected 2 items, got %v", body.Data["items"])
	}
	first := items[0].(map[string]any)
	if first["subtotal"].(float64) != 10_000 {
		t.Fatalf("subtotal mismatch: %v", first["subtotal"])
	}
	// PII / cross-tenant leak guards.
	raw := w.Body.String()
	for _, forbidden := range []string{
		tenant.ID,           // tenant_id must never appear
		fresh.ID,            // order primary key either
		"created_by",
		"employee_uuid",
		"branch_id",
		"customer_phone",
		"delivery_address",
	} {
		if forbidden == "" {
			continue
		}
		if contains(raw, forbidden) {
			t.Fatalf("response leaked forbidden field/value %q: %s", forbidden, raw)
		}
	}
}

func TestGetPublicTableSession_RejectsInvalidOrUnknownToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableSessionsDB(t)
	r := gin.New()
	r.GET("/api/v1/public/table-sessions/:session_token",
		GetPublicTableSession(db))

	// Not a UUID → 404 without DB roundtrip.
	if w := getJSON(r, "/api/v1/public/table-sessions/not-a-uuid"); w.Code != http.StatusNotFound {
		t.Fatalf("malformed token: want 404, got %d", w.Code)
	}
	// Valid-shaped but unknown → 404.
	if w := getJSON(r, "/api/v1/public/table-sessions/"+uuid.NewString()); w.Code != http.StatusNotFound {
		t.Fatalf("unknown token: want 404, got %d", w.Code)
	}
}

func TestGetPublicTableSession_GoneAfterClose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableSessionsDB(t)
	tenant := seedTenant(t, db, uuid.NewString(), "brasas")
	order := seedOpenOrder(t, db, tenant.ID, "Mesa 9")
	var fresh models.OrderTicket
	db.First(&fresh, "id = ?", order.ID)

	// Close the ticket — cashier path would normally do this via
	// UpdateOrderStatus, but we short-circuit to keep the test
	// focused on the public endpoint's response.
	db.Model(&fresh).Update("status", models.OrderStatusCobrado)

	r := gin.New()
	r.GET("/api/v1/public/table-sessions/:session_token",
		GetPublicTableSession(db))
	w := getJSON(r, "/api/v1/public/table-sessions/"+fresh.SessionToken)
	if w.Code != http.StatusGone {
		t.Fatalf("want 410 gone, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestCallWaiter_HappyPathAndRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableSessionsDB(t)
	tenant := seedTenant(t, db, uuid.NewString(), "brasas")
	order := seedOpenOrder(t, db, tenant.ID, "Mesa 3")
	var fresh models.OrderTicket
	db.First(&fresh, "id = ?", order.ID)

	r := gin.New()
	r.POST("/api/v1/public/table-sessions/:session_token/call-waiter",
		CallWaiter(db))

	// First call: 200 and writes the notification.
	w1 := postJSON(r, "/api/v1/public/table-sessions/"+fresh.SessionToken+"/call-waiter", nil)
	if w1.Code != http.StatusOK {
		t.Fatalf("first call: want 200, got %d body=%s", w1.Code, w1.Body.String())
	}

	var count int64
	db.Model(&models.Notification{}).
		Where("tenant_id = ? AND type = ?", tenant.ID, "waiter_call").
		Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 waiter_call notification, got %d", count)
	}

	// Immediate re-call: 429, no second notification.
	w2 := postJSON(r, "/api/v1/public/table-sessions/"+fresh.SessionToken+"/call-waiter", nil)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited call: want 429, got %d", w2.Code)
	}
	db.Model(&models.Notification{}).
		Where("tenant_id = ? AND type = ?", tenant.ID, "waiter_call").
		Count(&count)
	if count != 1 {
		t.Fatalf("expected rate limit to block notification, got count=%d", count)
	}

	// Rewind the timestamp past the 60 s window and re-call: 200.
	past := time.Now().Add(-2 * time.Minute).UTC()
	db.Model(&fresh).Update("waiter_called_at", past)
	w3 := postJSON(r, "/api/v1/public/table-sessions/"+fresh.SessionToken+"/call-waiter", nil)
	if w3.Code != http.StatusOK {
		t.Fatalf("post-cooldown call: want 200, got %d body=%s", w3.Code, w3.Body.String())
	}
}

func TestCallWaiter_GoneAfterClose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTableSessionsDB(t)
	tenant := seedTenant(t, db, uuid.NewString(), "brasas")
	order := seedOpenOrder(t, db, tenant.ID, "Mesa 3")
	var fresh models.OrderTicket
	db.First(&fresh, "id = ?", order.ID)
	db.Model(&fresh).Update("status", models.OrderStatusCobrado)

	r := gin.New()
	r.POST("/api/v1/public/table-sessions/:session_token/call-waiter",
		CallWaiter(db))
	w := postJSON(r, "/api/v1/public/table-sessions/"+fresh.SessionToken+"/call-waiter", nil)
	if w.Code != http.StatusGone {
		t.Fatalf("want 410 gone, got %d", w.Code)
	}
}

// contains is a thin wrapper so the leak-guard loop reads clearly.
func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
