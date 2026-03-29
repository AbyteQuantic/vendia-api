package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"vendia-backend/internal/auth"
	"vendia-backend/internal/middleware"
)

func TestSuperAdminOnly_BlocksRegularUser(t *testing.T) {
	gin.SetMode(gin.TestMode)

	token, _ := auth.GenerateToken("tenant-uuid", "3001234567", "Store", testSecret)

	r := gin.New()
	r.Use(middleware.Auth(testSecret))
	r.Use(middleware.SuperAdminOnly())
	r.GET("/admin", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestSuperAdminOnly_AllowsSuperAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	token, _ := auth.GenerateAdminToken("admin-uuid", "admin@vendia.co", "Admin", testSecret)

	r := gin.New()
	r.Use(middleware.Auth(testSecret))
	r.Use(middleware.SuperAdminOnly())
	r.GET("/admin", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}
