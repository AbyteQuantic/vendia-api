// Spec: specs/107-dashboard-v2-resumen/spec.md (FR-11)
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
)

func TestListProductsSearchQ(t *testing.T) {
	db := setupTestDB(t)
	tenant := seedSummaryTenant(t, db)

	for _, name := range []string{"Arroz Diana", "Aceite Girasol", "Arroz Roa"} {
		require.NoError(t, db.Create(&models.Product{
			TenantID: tenant.ID, Name: name, Price: 1000, Stock: 5, IsAvailable: true,
		}).Error)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/products", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenant.ID)
		c.Next()
	}, handlers.ListProducts(db))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/products?q=arroz", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var out struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Len(t, out.Data, 2, "solo los dos Arroz")
	for _, p := range out.Data {
		assert.Contains(t, p["name"].(string), "Arroz")
	}
}
