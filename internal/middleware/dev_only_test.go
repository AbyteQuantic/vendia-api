package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"vendia-backend/internal/middleware"
)

func TestDevOnly_AllowsInDevelopment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/seed", middleware.DevOnly("development"), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/seed", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestDevOnly_BlocksInProduction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/seed", middleware.DevOnly("production"), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/seed", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}
