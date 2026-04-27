package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/handlers"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Login flow under test (post-2026-04-27 RBAC hotfix):
//
//   path 1: users        — multi-workspace owner / staff with a User row
//   path 2: tenants      — legacy single-tenant owner
//   path 3: employees    — RBAC fallback, lazily upserts User+UserWorkspace
//
// Plus phone-format normalisation across all three paths.

const loginTestJWTSecret = "login-test-secret-at-least-32-chars-x"

func setupLoginDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
			phone TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL DEFAULT '',
			password_hash TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY, deleted_at DATETIME,
			business_name TEXT DEFAULT '',
			phone TEXT DEFAULT '',
			password_hash TEXT DEFAULT '',
			store_slug TEXT DEFAULT '',
			created_at DATETIME, updated_at DATETIME,
			-- Sale snapshot fields the createTokenPair helper expects
			feature_flags TEXT DEFAULT '{}',
			business_types TEXT DEFAULT '[]'
		);
		CREATE TABLE branches (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			name TEXT NOT NULL, address TEXT DEFAULT '',
			is_active INTEGER DEFAULT 1
		);
		CREATE TABLE user_workspaces (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME,
			user_id TEXT NOT NULL, tenant_id TEXT NOT NULL,
			branch_id TEXT, role TEXT NOT NULL DEFAULT 'owner',
			is_default INTEGER DEFAULT 0
		);
		CREATE TABLE employees (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			branch_id TEXT,
			name TEXT NOT NULL, phone TEXT DEFAULT '',
			pin TEXT DEFAULT '',
			role TEXT NOT NULL DEFAULT 'cashier',
			password_hash TEXT NOT NULL DEFAULT '',
			is_owner INTEGER DEFAULT 0,
			is_active INTEGER DEFAULT 1
		);
		CREATE TABLE refresh_tokens (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME,
			user_id TEXT, tenant_id TEXT,
			token TEXT NOT NULL,
			expires_at DATETIME NOT NULL,
			revoked INTEGER DEFAULT 0
		);
	`).Error)
	return db
}

func bcryptHash(t *testing.T, plain string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.MinCost)
	require.NoError(t, err)
	return string(h)
}

func mountLogin(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/login", handlers.Login(db, loginTestJWTSecret))
	return r
}

func postLogin(t *testing.T, r *gin.Engine, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// Path 3 — the bug the P.O. flagged in QA: an employee with a
// password set via the admin sheet but no User row (because of a
// failed upsert / legacy data) must still be able to log in.
func TestLogin_EmployeeWithoutUserRow_AuthenticatesAndUpserts(t *testing.T) {
	db := setupLoginDB(t)

	tenantID := uuid.NewString()
	branchID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, business_name, store_slug, created_at) VALUES (?, 'Don Brayan', 'tenda', datetime('now'))`,
		tenantID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO branches (id, tenant_id, name, is_active, created_at) VALUES (?, ?, 'Principal', 1, datetime('now'))`,
		branchID, tenantID,
	).Error)

	pwd := "viviana-12"
	require.NoError(t, db.Exec(
		`INSERT INTO employees (id, tenant_id, branch_id, name, phone, role, password_hash, is_owner, is_active, created_at) VALUES (?, ?, ?, 'Viviana', '3022798580', 'cashier', ?, 0, 1, datetime('now'))`,
		uuid.NewString(), tenantID, branchID, bcryptHash(t, pwd),
	).Error)

	w := postLogin(t, mountLogin(db), map[string]string{
		"phone":    "3022798580",
		"password": pwd,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// Side-effect: the lazy upsert created a User + UserWorkspace
	// pair so subsequent logins skip the employees fallback.
	var users int64
	db.Table("users").Where("phone = ?", "3022798580").Count(&users)
	assert.Equal(t, int64(1), users,
		"first employee login must lazily create the User row")

	var ws int64
	db.Table("user_workspaces").Where("tenant_id = ?", tenantID).Count(&ws)
	assert.Equal(t, int64(1), ws,
		"and pin a UserWorkspace linking to the right tenant")
}

// Phone format normalisation: stored verbatim "+57 302 279 8580" but
// the cashier types "3022798580" at login. Both must resolve.
func TestLogin_DigitsOnlyFallback(t *testing.T) {
	db := setupLoginDB(t)
	tenantID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, business_name, store_slug, created_at) VALUES (?, 'Don Brayan', 'tenda', datetime('now'))`,
		tenantID,
	).Error)
	pwd := "secreta-1"
	// Employee row with spaces + dashes — the format a tendero might
	// type into the admin form (matches what we see in QA).
	require.NoError(t, db.Exec(
		`INSERT INTO employees (id, tenant_id, name, phone, role, password_hash, is_owner, is_active, created_at) VALUES (?, ?, 'Pedro', '302 279-8580', 'cashier', ?, 0, 1, datetime('now'))`,
		uuid.NewString(), tenantID, bcryptHash(t, pwd),
	).Error)

	// Cashier types digits-only at login.
	w := postLogin(t, mountLogin(db), map[string]string{
		"phone":    "3022798580",
		"password": pwd,
	})
	assert.Equal(t, http.StatusOK, w.Code,
		"login must resolve via the digits-only fallback, not 401")
}

// Inactive employee path: the dueño disabled the row. Login must
// 403 with employee_inactive instead of leaking a 401.
func TestLogin_InactiveEmployee_Forbidden(t *testing.T) {
	db := setupLoginDB(t)
	tenantID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, business_name, created_at) VALUES (?, 'X', datetime('now'))`,
		tenantID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO employees (id, tenant_id, name, phone, role, password_hash, is_owner, is_active, created_at) VALUES (?, ?, 'A', '3001112222', 'cashier', ?, 0, 0, datetime('now'))`,
		uuid.NewString(), tenantID, bcryptHash(t, "x"),
	).Error)

	w := postLogin(t, mountLogin(db), map[string]string{
		"phone": "3001112222", "password": "x",
	})
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "employee_inactive")
}

// Wrong password against the same employee path returns the canonical
// 401 — never leaks "employee row exists with a different password".
func TestLogin_WrongPassword_Returns401(t *testing.T) {
	db := setupLoginDB(t)
	tenantID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, business_name, created_at) VALUES (?, 'X', datetime('now'))`,
		tenantID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO employees (id, tenant_id, name, phone, role, password_hash, is_owner, is_active, created_at) VALUES (?, ?, 'A', '3001112222', 'cashier', ?, 0, 1, datetime('now'))`,
		uuid.NewString(), tenantID, bcryptHash(t, "right-one"),
	).Error)

	w := postLogin(t, mountLogin(db), map[string]string{
		"phone": "3001112222", "password": "wrong",
	})
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
