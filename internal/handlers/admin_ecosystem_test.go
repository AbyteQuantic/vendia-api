package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ── MaskPhone ───────────────────────────────────────────────────────────────

func TestMaskPhone_PreservesCountryCodeAndLast4(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"colombia mobile with +57",
			"+573001234567", "+57 300 *** 4567"},
		{"domestic 10-digit",
			"3001234567", "300 *** 4567"},
		{"phone with whitespace and plus",
			" +57 300 123 4567 ", "+57 300 *** 4567"},
		{"empty string stays empty",
			"", ""},
		{"short phone is not masked (less info to leak than mask produces)",
			"1234", "1234"},
		{"international non-CO still masks middle (domestic format fallback)",
			"+14155551234", "141 *** 1234"}, // 11 digits — falls through to n>=10 branch
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, handlers.MaskPhone(tc.in))
		})
	}
}

func TestMaskPhone_ColombianMobileExact(t *testing.T) {
	// The brief calls out this exact format.
	assert.Equal(t, "+57 300 *** 4567", handlers.MaskPhone("+573001234567"))
	assert.Equal(t, "300 *** 4567", handlers.MaskPhone("3001234567"))
}

// ── Cross-identity grouping + evasion ────────────────────────────────────────

func TestBuildCrossIdentityRecords_GroupsParticipationsByUser(t *testing.T) {
	rows := []handlers.CrossIdentityRow{
		// user-1 is owner of Free Tenant + cashier of PRO Tenant → flag
		{UserID: "user-1", Phone: "+573001234567", TenantID: "tenant-free",
			BusinessName: "Miscelánea Juan", Role: string(models.RoleOwner),
			SubscriptionStatus: models.SubscriptionStatusFree},
		{UserID: "user-1", Phone: "+573001234567", TenantID: "tenant-pro",
			BusinessName: "Restaurante Ana", Role: string(models.RoleWSCashier),
			SubscriptionStatus: models.SubscriptionStatusProActive},
		// user-2 is owner of two PRO tenants → no flag (both premium)
		{UserID: "user-2", Phone: "+573009876543", TenantID: "tenant-pro-2",
			BusinessName: "Tienda A", Role: string(models.RoleOwner),
			SubscriptionStatus: models.SubscriptionStatusProActive},
		{UserID: "user-2", Phone: "+573009876543", TenantID: "tenant-pro-3",
			BusinessName: "Tienda B", Role: string(models.RoleOwner),
			SubscriptionStatus: models.SubscriptionStatusProActive},
	}

	records := handlers.BuildCrossIdentityRecords(rows)

	require.Len(t, records, 2)

	u1 := records[0]
	assert.Equal(t, "user-1", u1.UserID)
	assert.Equal(t, "+57 300 *** 4567", u1.PhoneMasked)
	assert.Equal(t, 2, u1.WorkspaceCount)
	assert.Len(t, u1.Participations, 2)
	assert.True(t, u1.EvasionAlert,
		"Owner in FREE + Cashier in PRO must trigger the ⚠️ flag")
	assert.Contains(t, u1.EvasionReason, "posible evasión")

	u2 := records[1]
	assert.Equal(t, "user-2", u2.UserID)
	assert.Equal(t, 2, u2.WorkspaceCount)
	assert.False(t, u2.EvasionAlert,
		"Two PRO tenants with Owner role are legit multi-business users")
}

func TestBuildCrossIdentityRecords_NoAlertWhenOwnerOfOnlyPro(t *testing.T) {
	records := handlers.BuildCrossIdentityRecords([]handlers.CrossIdentityRow{
		{UserID: "owner", Phone: "+573001112222", TenantID: "t-a",
			BusinessName: "Tienda A", Role: string(models.RoleOwner),
			SubscriptionStatus: models.SubscriptionStatusProActive},
		{UserID: "owner", Phone: "+573001112222", TenantID: "t-b",
			BusinessName: "Tienda B", Role: string(models.RoleWSCashier),
			SubscriptionStatus: models.SubscriptionStatusProActive},
	})
	require.Len(t, records, 1)
	assert.False(t, records[0].EvasionAlert,
		"Owner of PRO + Cashier of PRO is not suspicious — everyone is paying")
}

func TestBuildCrossIdentityRecords_EmptyInputReturnsEmpty(t *testing.T) {
	records := handlers.BuildCrossIdentityRecords(nil)
	assert.Empty(t, records)
}

// ── End-to-end SQLite tests for the query + metrics handler ──────────────────

func setupEcosystemDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// Hand-crafted DDL — the Tenant/CreditAccount/etc models carry
	// Postgres-specific defaults (gen_random_uuid, jsonb) so we narrow
	// the schema to just the columns this feature needs.
	stmts := []string{
		`CREATE TABLE tenants (id TEXT PRIMARY KEY, deleted_at DATETIME,
			business_name TEXT NOT NULL DEFAULT '', created_at DATETIME)`,
		`CREATE TABLE users (id TEXT PRIMARY KEY, deleted_at DATETIME,
			phone TEXT NOT NULL, name TEXT DEFAULT '', created_at DATETIME)`,
		`CREATE TABLE user_workspaces (id TEXT PRIMARY KEY, deleted_at DATETIME,
			user_id TEXT NOT NULL, tenant_id TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'owner', created_at DATETIME)`,
		`CREATE TABLE credit_accounts (id TEXT PRIMARY KEY, deleted_at DATETIME,
			tenant_id TEXT NOT NULL, customer_id TEXT NOT NULL,
			total_amount INTEGER NOT NULL DEFAULT 0,
			paid_amount INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'open', created_at DATETIME)`,
		`CREATE TABLE online_orders (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL,
			customer_name TEXT NOT NULL DEFAULT '', status TEXT DEFAULT 'pending',
			total_amount REAL DEFAULT 0, created_at DATETIME, updated_at DATETIME)`,
	}
	for _, s := range stmts {
		require.NoError(t, db.Exec(s).Error)
	}
	require.NoError(t, db.AutoMigrate(&models.TenantSubscription{}))
	return db
}

func TestAdminCrossIdentities_E2E_DetectsSharedPhoneAcrossTenants(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupEcosystemDB(t)

	now := time.Now().UTC()
	// Two tenants: one FREE, one PRO_ACTIVE.
	require.NoError(t, db.Exec(`
		INSERT INTO tenants (id, business_name, created_at) VALUES
		('t-free','Miscelánea Juan',?),
		('t-pro', 'Restaurante Ana',?),
		('t-pro-solo','Tienda Solitaria',?)
	`, now, now, now).Error)
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: "t-free", Status: models.SubscriptionStatusFree,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: "t-pro", Status: models.SubscriptionStatusProActive,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: "t-pro-solo", Status: models.SubscriptionStatusProActive,
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	// Two users: shared owner→cashier across free+pro, plus a solo user
	// that only appears once and must NOT be returned.
	require.NoError(t, db.Exec(`
		INSERT INTO users (id, phone, created_at) VALUES
		('user-1','+573001234567',?),
		('user-2','+573009998888',?)
	`, now, now).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO user_workspaces (id, user_id, tenant_id, role, created_at) VALUES
		('ws1','user-1','t-free','owner',?),
		('ws2','user-1','t-pro','cashier',?),
		('ws3','user-2','t-pro-solo','owner',?)
	`, now, now, now).Error)

	r := gin.New()
	r.GET("/api/v1/admin/ecosystem/cross-identities",
		handlers.AdminCrossIdentities(db))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet,
		"/api/v1/admin/ecosystem/cross-identities", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Data []handlers.CrossIdentityRecord `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	// Only user-1 has workspaces in >1 tenant — user-2's solo record
	// is excluded by the HAVING clause.
	require.Len(t, body.Data, 1)
	rec := body.Data[0]
	assert.Equal(t, "user-1", rec.UserID)
	assert.Equal(t, "+57 300 *** 4567", rec.PhoneMasked)
	assert.Equal(t, 2, rec.WorkspaceCount)
	assert.True(t, rec.EvasionAlert,
		"Owner-in-Free + Cashier-in-PRO must surface the ⚠️")
}

func TestAdminEcosystemMetrics_AggregatesFiadoOrdersAndDebt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupEcosystemDB(t)
	now := time.Now().UTC()

	require.NoError(t, db.Exec(`
		INSERT INTO tenants (id, business_name, created_at) VALUES
		('t1','T1',?),('t2','T2',?)
	`, now, now).Error)

	// Fiado: 2 open, 1 partial, 1 closed, 1 paid
	require.NoError(t, db.Exec(`
		INSERT INTO credit_accounts
			(id, tenant_id, customer_id, total_amount, paid_amount, status, created_at)
		VALUES
			('c1','t1','cust1',10000,0,    'open',    ?),
			('c2','t1','cust2',20000,5000, 'partial', ?),
			('c3','t2','cust3',15000,15000,'closed',  ?),
			('c4','t2','cust4',8000, 8000, 'paid',    ?),
			('c5','t2','cust5',50000,0,    'open',    ?)
	`, now, now, now, now, now).Error)

	require.NoError(t, db.Exec(`
		INSERT INTO online_orders (id, tenant_id, customer_name, created_at, updated_at)
		VALUES ('o1','t1','C1',?,?),('o2','t2','C2',?,?),('o3','t1','C3',?,?)
	`, now, now, now, now, now, now).Error)

	r := gin.New()
	r.GET("/api/v1/admin/ecosystem/metrics", handlers.AdminEcosystemMetrics(db))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet,
		"/api/v1/admin/ecosystem/metrics", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body handlers.EcosystemMetrics
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	assert.EqualValues(t, 3, body.FiadoOpenCount, "open + partial count together")
	assert.EqualValues(t, 2, body.FiadoClosedCount, "closed + paid count together")
	assert.EqualValues(t, 3, body.OnlineOrdersCount)
	// Outstanding: c1 (10k - 0) + c2 (20k - 5k) + c5 (50k - 0) = 75000
	assert.EqualValues(t, 75000, body.OutstandingDebtCOP)
}

