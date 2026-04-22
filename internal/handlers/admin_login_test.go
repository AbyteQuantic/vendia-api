package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/auth"
	"vendia-backend/internal/handlers"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const adminTestJWTSecret = "admin-test-secret-at-least-32-chars-long"

func setupAdminLoginDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE admin_users (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME,
			email TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			name TEXT NOT NULL,
			is_super_admin INTEGER DEFAULT 1
		);
		CREATE UNIQUE INDEX idx_admin_users_email ON admin_users(email);
	`).Error)
	return db
}

func seedAdmin(t *testing.T, db *gorm.DB, email, password string, isSuper bool) models.AdminUser {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	// Use raw SQL so GORM doesn't helpfully apply the `default:true`
	// tag on IsSuperAdmin when we pass false — the downgraded-admin
	// test case depends on writing a literal 0 for is_super_admin.
	isSuperInt := 0
	if isSuper {
		isSuperInt = 1
	}
	require.NoError(t, db.Exec(`
		INSERT INTO admin_users (id, created_at, updated_at, email,
			password_hash, name, is_super_admin)
		VALUES (?, datetime('now'), datetime('now'), ?, ?, 'Test Admin', ?)
	`, "admin-"+email, email, string(hash), isSuperInt).Error)

	return models.AdminUser{
		Email: email, PasswordHash: string(hash),
		Name: "Test Admin", IsSuperAdmin: isSuper,
	}
}

func mountAdminLogin(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/admin/login", handlers.AdminLogin(db, adminTestJWTSecret))
	return r
}

func postAdminLogin(t *testing.T, r *gin.Engine, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost,
		"/api/v1/admin/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestAdminLogin_Success_IssuesSuperAdminJWT(t *testing.T) {
	db := setupAdminLoginDB(t)
	seedAdmin(t, db, "bryan@vendia.co", "super-secret-42", true)

	r := mountAdminLogin(db)
	w := postAdminLogin(t, r, map[string]string{
		"email":    "bryan@vendia.co",
		"password": "super-secret-42",
	})
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, true, body["is_super_admin"])
	assert.Equal(t, "super_admin", body["role"])

	token, ok := body["token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, token)

	claims, err := auth.ValidateToken(token, adminTestJWTSecret)
	require.NoError(t, err)
	assert.True(t, claims.IsSuperAdmin,
		"the JWT claim must flip IsSuperAdmin so SuperAdminOnly() lets the caller through")
}

func TestAdminLogin_WrongPassword(t *testing.T) {
	db := setupAdminLoginDB(t)
	seedAdmin(t, db, "bryan@vendia.co", "correct", true)

	r := mountAdminLogin(db)
	w := postAdminLogin(t, r, map[string]string{
		"email":    "bryan@vendia.co",
		"password": "totally-wrong",
	})
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "incorrectos")
}

func TestAdminLogin_UnknownEmail(t *testing.T) {
	db := setupAdminLoginDB(t)
	r := mountAdminLogin(db)

	w := postAdminLogin(t, r, map[string]string{
		"email":    "ghost@nowhere.io",
		"password": "anything",
	})
	// Same status as wrong-password so an attacker can't enumerate
	// valid emails through the response code.
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "incorrectos")
}

func TestAdminLogin_NormalisesEmailBeforeLookup(t *testing.T) {
	db := setupAdminLoginDB(t)
	seedAdmin(t, db, "bryan@vendia.co", "hello", true)

	r := mountAdminLogin(db)
	w := postAdminLogin(t, r, map[string]string{
		"email":    "  BRYAN@VENDIA.CO  ",
		"password": "hello",
	})
	assert.Equal(t, http.StatusOK, w.Code,
		"leading whitespace and uppercase must still resolve to the stored row")
}

func TestAdminLogin_NonSuperAdminRowIsRejected(t *testing.T) {
	db := setupAdminLoginDB(t)
	// Row exists but is_super_admin=false — that's a downgraded
	// account the endpoint must not issue a super-admin JWT for.
	seedAdmin(t, db, "ops@vendia.co", "p", false)

	r := mountAdminLogin(db)
	w := postAdminLogin(t, r, map[string]string{
		"email":    "ops@vendia.co",
		"password": "p",
	})
	assert.Equal(t, http.StatusUnauthorized, w.Code,
		"downgraded admin rows must not receive a super-admin token")
}

func TestAdminLogin_RejectsMissingFields(t *testing.T) {
	db := setupAdminLoginDB(t)
	r := mountAdminLogin(db)

	for _, body := range []map[string]string{
		{"email": "x@y.co"},              // no password
		{"password": "x"},                // no email
		{"email": "", "password": "x"},   // empty email string
	} {
		w := postAdminLogin(t, r, body)
		assert.Equal(t, http.StatusBadRequest, w.Code,
			"body %v must return 400", body)
	}
}
