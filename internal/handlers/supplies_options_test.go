// Spec: specs/077-compra-inteligente-insumos/spec.md
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

func TestSupplyOptions_PackagingAndRecommended(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.IngredientPrice{}, &models.Tenant{}, &models.Ingredient{}, &models.ChainPrice{}))
	// dos proveedores: uno manual (garantizado) barato, otro factura (estimado).
	require.NoError(t, db.Create(&models.IngredientPrice{BaseModel: models.BaseModel{ID: "a"}, TenantID: "t1", IngredientID: sp("crema"), Source: models.PriceSourceManual, UnitPrice: 4200, PackUnit: "ml", PackQty: 1000, PricePerBaseUnit: 4.2, RawName: "Crema x 1L", SupplierName: "Don Pepe"}).Error)
	require.NoError(t, db.Create(&models.IngredientPrice{BaseModel: models.BaseModel{ID: "b"}, TenantID: "t1", IngredientID: sp("crema"), Source: models.PriceSourceInvoiceOCR, UnitPrice: 9000, PackUnit: "ml", PackQty: 1000, PricePerBaseUnit: 9, RawName: "Crema x 1L caro"}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.GET("/supplies/options", handlers.SupplyOptions(db))

	w := doJSON(t, r, http.MethodGet, "/supplies/options?ingredient_id=crema&name=Crema&unit=ml&shortfall=2", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Options []handlers.PriceOption `json:"options"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.GreaterOrEqual(t, len(resp.Data.Options), 2)
	// cada opción con empaque conocido: 1 bolsa, costo del empaque entero.
	var manual *handlers.PriceOption
	for i := range resp.Data.Options {
		if resp.Data.Options[i].Supplier == "Don Pepe" {
			manual = &resp.Data.Options[i]
		}
	}
	require.NotNil(t, manual)
	require.NotNil(t, manual.Packs)
	assert.Equal(t, 1, *manual.Packs)
	assert.InDelta(t, 4200, manual.Cost, 0.001)
	assert.InDelta(t, 998, manual.Leftover, 0.001)
	assert.True(t, manual.Recommended) // manual barato = recomendado (no estimado)
	assert.False(t, manual.IsEstimate)
}

func sp(s string) *string { return &s }

// Spec 077 — el insumo en kg y el empaque en g deben CUADRAR (conversión).
func TestSupplyOptions_UnitConversion_KgVsG(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.IngredientPrice{}, &models.Tenant{}, &models.Ingredient{}, &models.ChainPrice{}))
	// Proveedor vende arroz en bolsa de 1000 g (=1kg) a $5000.
	require.NoError(t, db.Create(&models.IngredientPrice{BaseModel: models.BaseModel{ID: "a"}, TenantID: "t1", IngredientID: sp("arroz"), Source: models.PriceSourceManual, UnitPrice: 5000, PackUnit: "g", PackQty: 1000, RawName: "Arroz x 1kg", SupplierName: "Don Pepe"}).Error)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.GET("/supplies/options", handlers.SupplyOptions(db))
	// Faltan 2 KG → debe comprar 2 bolsas de 1kg = $10000 (no descuadrar por 1000x).
	w := doJSON(t, r, http.MethodGet, "/supplies/options?ingredient_id=arroz&name=Arroz&unit=kg&shortfall=2", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Options []handlers.PriceOption `json:"options"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.GreaterOrEqual(t, len(resp.Data.Options), 1)
	o := resp.Data.Options[0]
	require.NotNil(t, o.Packs)
	assert.Equal(t, 2, *o.Packs)            // 2 kg / 1000 g = 2 bolsas
	assert.InDelta(t, 10000, o.Cost, 0.001) // 2 × $5000
	assert.False(t, o.PackUnknown)
}
