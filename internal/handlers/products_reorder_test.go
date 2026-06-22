// Spec: specs/078-centro-tareas-unificado/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestProductReorderList_LowStockOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}, &models.Branch{}))
	// bajo mínimo → entra; con stock suficiente → no entra; sin min_stock → no entra.
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p1"}, TenantID: "t1", Name: "Gaseosa", Stock: 1, MinStock: 10, PurchasePrice: 2000, IsAvailable: true}).Error)
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p2"}, TenantID: "t1", Name: "Pan", Stock: 50, MinStock: 10, IsAvailable: true}).Error)
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p3"}, TenantID: "t1", Name: "Sal", Stock: 0, MinStock: 0, IsAvailable: true}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.GET("/products/reorder-list", handlers.ProductReorderList(db))
	w := doJSON(t, r, http.MethodGet, "/products/reorder-list", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Items, 1)
	assert.Equal(t, "Gaseosa", resp.Data.Items[0]["name"])
	assert.Equal(t, "product", resp.Data.Items[0]["line_kind"])
	assert.Equal(t, float64(9), resp.Data.Items[0]["shortfall"]) // 10 - 1
}
