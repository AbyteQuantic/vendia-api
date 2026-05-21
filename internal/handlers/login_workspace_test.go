package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/auth"
	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Per-workspace password flow:
//
//   1. Login: typing ANY of the user's valid passwords (User OR
//      Employee) unlocks a permissive selector listing every
//      workspace they belong to. The response carries
//      `requires_workspace_password: true` so the Flutter client
//      knows to prompt again on tap.
//   2. SelectWorkspace: the chosen workspace's binding credential
//      (Employee.password_hash for that tenant if present, else
//      User.password_hash) MUST match the typed password. Cross-
//      tenant credential reuse is rejected with
//      `workspace_password_mismatch`.

func mountLoginAndSelect(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/login", handlers.Login(db, loginTestJWTSecret))
	authGroup := r.Group("/")
	authGroup.Use(middleware.Auth(loginTestJWTSecret))
	authGroup.POST("/select-workspace", handlers.SelectWorkspace(db, loginTestJWTSecret))
	return r
}

// seedMultiWorkspaceUser builds the canonical "Viviana" fixture:
// owner of Tienda V (pwd = ownerPwd) AND cashier at Don Brayan's
// (pwd = cashierPwd assigned by Brayan, different bytes).
func seedMultiWorkspaceUser(
	t *testing.T,
	db *gorm.DB,
	phone, ownerPwd, cashierPwd string,
) (userID, ownerTenantID, cashierTenantID, ownerWSID, cashierWSID string) {
	t.Helper()

	userID = uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO users (id, phone, name, password_hash, created_at) VALUES (?, ?, 'Viviana', ?, datetime('now'))`,
		userID, phone, bcryptHash(t, ownerPwd),
	).Error)

	ownerTenantID = uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, business_name, store_slug, created_at) VALUES (?, 'Tienda V', 'vivi', datetime('now'))`,
		ownerTenantID,
	).Error)
	// Owner Employee row carries the same hash as the User row
	// (the user IS the owner).
	require.NoError(t, db.Exec(
		`INSERT INTO employees (id, tenant_id, name, phone, role, password_hash, is_owner, is_active, created_at) VALUES (?, ?, 'Viviana', ?, 'admin', ?, 1, 1, datetime('now'))`,
		uuid.NewString(), ownerTenantID, phone, bcryptHash(t, ownerPwd),
	).Error)
	ownerWSID = uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO user_workspaces (id, user_id, tenant_id, role, created_at) VALUES (?, ?, ?, 'owner', datetime('now'))`,
		ownerWSID, userID, ownerTenantID,
	).Error)

	cashierTenantID = uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, business_name, store_slug, created_at) VALUES (?, 'Don Brayan', 'brayan', datetime('now'))`,
		cashierTenantID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO employees (id, tenant_id, name, phone, role, password_hash, is_owner, is_active, created_at) VALUES (?, ?, 'Viviana', ?, 'cashier', ?, 0, 1, datetime('now'))`,
		uuid.NewString(), cashierTenantID, phone, bcryptHash(t, cashierPwd),
	).Error)
	cashierWSID = uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO user_workspaces (id, user_id, tenant_id, role, created_at) VALUES (?, ?, ?, 'cashier', datetime('now'))`,
		cashierWSID, userID, cashierTenantID,
	).Error)
	return
}

// MULTI-WORKSPACE: typing ANY valid identity password returns the
// selector + temp_token + requires_workspace_password=true. The user
// must hit /select-workspace next.
func TestLogin_MultiWorkspace_ReturnsSelectorWithFlag(t *testing.T) {
	db := setupLoginDB(t)
	phone := "3022798580"
	seedMultiWorkspaceUser(t, db, phone, "owner-pw", "cashpw01")

	w := postLogin(t, mountLoginAndSelect(db), map[string]string{
		"phone":    phone,
		"password": "owner-pw",
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["requires_workspace_password"],
		"multi-workspace response must signal a second password prompt")
	assert.NotEmpty(t, resp["temp_token"], "client needs the temp_token to call /select-workspace")
	wss, _ := resp["workspaces"].([]any)
	assert.Len(t, wss, 2,
		"selector must list every workspace the user belongs to (no filtering by which password matched)")
}

// SELECT-WORKSPACE happy path: temp_token + password matching the
// chosen workspace's credential mints the final JWT.
func TestSelectWorkspace_CorrectPassword_MintsJWT(t *testing.T) {
	db := setupLoginDB(t)
	phone := "3022798580"
	userID, _, _, _, cashierWSID := seedMultiWorkspaceUser(t, db, phone, "owner-pw", "cashpw01")

	tempToken, err := auth.GenerateToken(userID, phone, "", loginTestJWTSecret)
	require.NoError(t, err)

	body, _ := json.Marshal(map[string]string{
		"workspace_id": cashierWSID,
		"password":     "cashpw01",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/select-workspace", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tempToken)
	req.Header.Set("Content-Type", "application/json")
	mountLoginAndSelect(db).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp["access_token"], "must mint the workspace-scoped JWT")
}

// SELECT-WORKSPACE credential boundary: typing the OWNER's password
// to enter the CASHIER workspace at Don Brayan's must fail. Tienda
// A's credential cannot mint a JWT for Tienda B.
func TestSelectWorkspace_CrossTenantPassword_Rejected(t *testing.T) {
	db := setupLoginDB(t)
	phone := "3022798580"
	userID, _, _, _, cashierWSID := seedMultiWorkspaceUser(t, db, phone, "owner-pw", "cashpw01")

	tempToken, err := auth.GenerateToken(userID, phone, "", loginTestJWTSecret)
	require.NoError(t, err)

	body, _ := json.Marshal(map[string]string{
		"workspace_id": cashierWSID,
		"password":     "owner-pw",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/select-workspace", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tempToken)
	req.Header.Set("Content-Type", "application/json")
	mountLoginAndSelect(db).ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code,
		"owner-pw must NOT open the cashier workspace at a different tenant")
	assert.Contains(t, w.Body.String(), "workspace_password_mismatch")
}

// SELECT-WORKSPACE wrong password: random gibberish against any
// workspace fails with the same code.
func TestSelectWorkspace_WrongPassword_Rejected(t *testing.T) {
	db := setupLoginDB(t)
	phone := "3022798580"
	userID, _, _, ownerWSID, _ := seedMultiWorkspaceUser(t, db, phone, "owner-pw", "cashpw01")

	tempToken, err := auth.GenerateToken(userID, phone, "", loginTestJWTSecret)
	require.NoError(t, err)

	body, _ := json.Marshal(map[string]string{
		"workspace_id": ownerWSID,
		"password":     "wrongpwd",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/select-workspace", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tempToken)
	req.Header.Set("Content-Type", "application/json")
	mountLoginAndSelect(db).ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "workspace_password_mismatch")
}

// SELECT-WORKSPACE missing password: request validation rejects with
// 400 — the binding tag is what enforces the prompt at the API layer.
func TestSelectWorkspace_MissingPassword_400(t *testing.T) {
	db := setupLoginDB(t)
	phone := "3022798580"
	userID, _, _, ownerWSID, _ := seedMultiWorkspaceUser(t, db, phone, "owner-pw", "cashpw01")

	tempToken, err := auth.GenerateToken(userID, phone, "", loginTestJWTSecret)
	require.NoError(t, err)

	body, _ := json.Marshal(map[string]string{
		"workspace_id": ownerWSID,
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/select-workspace", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tempToken)
	req.Header.Set("Content-Type", "application/json")
	mountLoginAndSelect(db).ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// SINGLE WORKSPACE: with one workspace, the credential boundary still
// applies — typing the User.password_hash mints a JWT directly only
// when it ALSO matches the workspace-binding credential (Employee row
// for that tenant). When User.password_hash matches but Employee
// hash differs, login MUST go through the selector instead of
// minting an unauthorised JWT.
func TestLogin_SingleWorkspace_UserPwdNotMatchingEmployee_FallsToSelector(t *testing.T) {
	db := setupLoginDB(t)

	tenantID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, business_name, store_slug, created_at) VALUES (?, 'Don Brayan', 'tt', datetime('now'))`,
		tenantID,
	).Error)

	phone := "3022798580"
	personalPwd := "globpwd1"
	tenantAssignedPwd := "brayanpw"

	userID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO users (id, phone, name, password_hash, created_at) VALUES (?, ?, 'Viviana', ?, datetime('now'))`,
		userID, phone, bcryptHash(t, personalPwd),
	).Error)
	// One workspace — but the binding credential for it is the
	// Employee.password_hash, which differs from User.password_hash.
	require.NoError(t, db.Exec(
		`INSERT INTO user_workspaces (id, user_id, tenant_id, role, created_at) VALUES (?, ?, ?, 'cashier', datetime('now'))`,
		uuid.NewString(), userID, tenantID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO employees (id, tenant_id, name, phone, role, password_hash, is_owner, is_active, created_at) VALUES (?, ?, 'Viviana', ?, 'cashier', ?, 0, 1, datetime('now'))`,
		uuid.NewString(), tenantID, phone, bcryptHash(t, tenantAssignedPwd),
	).Error)

	// Type the global password — identity matches but the workspace
	// credential is Brayan's per-tenant hash.
	w := postLogin(t, mountLoginAndSelect(db), map[string]string{
		"phone":    phone,
		"password": personalPwd,
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["requires_workspace_password"],
		"global password matches identity but not the workspace credential — must surface the selector instead of minting an unauthorised JWT")
}
