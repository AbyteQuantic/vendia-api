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

// La query fusionada (SUM + COUNT FILTER en una pasada) debe dar los mismos
// números que las 4 queries previas. Audit 2026-06-24.
func TestInventoryHealth_FusedAggregates(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}))

	mk := func(id string, stock, min int, purchase, price float64, available bool) models.Product {
		return models.Product{
			BaseModel: models.BaseModel{ID: id}, TenantID: "t1", Name: id,
			Stock: stock, MinStock: min, PurchasePrice: purchase, Price: price, IsAvailable: available,
		}
	}
	require.NoError(t, db.Create(&[]models.Product{
		mk("p1", 10, 5, 100, 150, true), // cost 1000 retail 1500 — ok
		mk("p2", 2, 5, 200, 300, true),  // cost 400 retail 600 — LOW
		mk("p3", 0, 3, 50, 80, true),    // cost 0 — LOW + OUT
		mk("p4", 5, 0, 10, 20, true),    // cost 50 retail 100 — min_stock 0 → ni low
		mk("p5", 9, 5, 999, 999, false), // no disponible → excluido
	}).Error)
	// GORM omite el bool false (default:true en el modelo) al crear desde struct,
	// así que forzamos p5 a no-disponible con un Update explícito de la columna.
	require.NoError(t, db.Model(&models.Product{}).Where("id = ?", "p5").Update("is_available", false).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.GET("/analytics/inventory-health", handlers.InventoryHealth(db))

	w := doJSON(t, r, http.MethodGet, "/analytics/inventory-health", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Cost            float64 `json:"inventory_cost_value"`
			Retail          float64 `json:"inventory_retail_value"`
			Profit          float64 `json:"potential_profit"`
			LowStockCount   int64   `json:"low_stock_count"`
			OutOfStockCount int64   `json:"out_of_stock_count"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 1450.0, resp.Data.Cost)
	assert.Equal(t, 2200.0, resp.Data.Retail)
	assert.Equal(t, 750.0, resp.Data.Profit)
	assert.Equal(t, int64(2), resp.Data.LowStockCount)
	assert.Equal(t, int64(1), resp.Data.OutOfStockCount)
}
