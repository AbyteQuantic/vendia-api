// Spec: specs/077-compra-inteligente-insumos/spec.md
package handlers_test

import (
	"net/http"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAddSupplyPrice_NormalizesPack(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.IngredientPrice{}))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.POST("/supplies/prices", handlers.AddSupplyPrice(db))

	// Bulto de 50 kg a $85.000 → price_per_base_unit = 1.700/kg.
	w := doJSON(t, r, http.MethodPost, "/supplies/prices", map[string]any{
		"ingredient_id": "arroz", "raw_name": "Arroz", "supplier_name": "Mi mayorista",
		"unit_price": 85000, "pack_unit": "kg", "pack_qty": 50,
	})
	require.Equal(t, http.StatusCreated, w.Code)

	var saved models.IngredientPrice
	require.NoError(t, db.First(&saved, "tenant_id = ?", "t1").Error)
	assert.Equal(t, models.PriceSourceManual, saved.Source)
	assert.InDelta(t, 1700, saved.PricePerBaseUnit, 0.001)
	assert.InDelta(t, 0.8, saved.Confidence, 0.001)

	// Y el precio sugerido lo toma (manual = no estimado).
	sp := services.SuggestIngredientPrice(db, "t1", "", "arroz", 0)
	assert.InDelta(t, 1700, sp.PricePerUnit, 0.001)
	assert.Equal(t, models.PriceSourceManual, sp.Source)
	assert.False(t, sp.IsEstimate)

	// Precio inválido → 400.
	bad := doJSON(t, r, http.MethodPost, "/supplies/prices", map[string]any{"ingredient_id": "arroz", "unit_price": 0})
	assert.Equal(t, http.StatusBadRequest, bad.Code)
}

func TestAddSupplyPricesFromInvoice(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.IngredientPrice{}))
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.POST("/supplies/prices/from-invoice", handlers.AddSupplyPricesFromInvoice(db))

	w := doJSON(t, r, http.MethodPost, "/supplies/prices/from-invoice", map[string]any{
		"invoice_ref": "FAC-123", "supplier_name": "Distribuidora X",
		"items": []map[string]any{
			{"ingredient_id": "arroz", "raw_name": "Arroz Diana 5kg", "unit_price": 13000, "pack_unit": "kg", "pack_qty": 5},
			{"ingredient_id": "", "raw_name": "sin match", "unit_price": 9000}, // sin ingredient_id → se ignora
		},
	})
	require.Equal(t, http.StatusCreated, w.Code)
	var saved []models.IngredientPrice
	db.Find(&saved)
	require.Len(t, saved, 1) // solo el confirmado
	assert.Equal(t, models.PriceSourceInvoiceOCR, saved[0].Source)
	assert.InDelta(t, 2600, saved[0].PricePerBaseUnit, 0.001)                            // 13000/5
	assert.True(t, services.SuggestIngredientPrice(db, "t1", "", "arroz", 0).IsEstimate) // factura = estimado
}
