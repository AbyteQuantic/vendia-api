// Spec: specs/089-keepalive-free-tier/spec.md
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/handlers"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestHealthDB_OK(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/healthz/db", handlers.HealthDB(db))

	req := httptest.NewRequest(http.MethodGet, "/healthz/db", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"db":"up"`)
}
