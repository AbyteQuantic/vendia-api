// Spec: specs/077-compra-inteligente-insumos/spec.md
package handlers_test

import (
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

func TestReceiveErrand_EntersStockAndCost(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Ingredient{}, &models.PurchaseErrand{},
		&models.PurchaseErrandLine{}, &models.InventoryMovement{}))
	require.NoError(t, db.Create(&models.Ingredient{BaseModel: models.BaseModel{ID: "arroz"}, TenantID: "t1", Name: "Arroz", Unit: "kg", Stock: 0, UnitCost: 0}).Error)
	require.NoError(t, db.Create(&models.PurchaseErrand{BaseModel: models.BaseModel{ID: "er1"}, TenantID: "t1", Status: "pendiente"}).Error)
	iid := "arroz"
	require.NoError(t, db.Create(&models.PurchaseErrandLine{BaseModel: models.BaseModel{ID: "l1"}, ErrandID: "er1", IngredientID: &iid, Name: "Arroz", Unit: "kg", Qty: 3, EstimatedUnitPrice: 2800}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.POST("/errands/:id/receive", handlers.ReceiveErrand(db))

	w := doJSON(t, r, http.MethodPost, "/errands/er1/receive", nil)
	require.Equal(t, http.StatusOK, w.Code)

	// stock subió, costo = precio de la línea, kardex con purchase_receipt, mandado comprado.
	var ing models.Ingredient
	require.NoError(t, db.First(&ing, "id = ?", "arroz").Error)
	assert.Equal(t, 3.0, ing.Stock)
	assert.Equal(t, 2800.0, ing.UnitCost)
	var movs int64
	db.Model(&models.InventoryMovement{}).Where("movement_type = ? AND product_id = ?", models.MovementPurchaseReceipt, "arroz").Count(&movs)
	assert.Equal(t, int64(1), movs)
	var er models.PurchaseErrand
	require.NoError(t, db.First(&er, "id = ?", "er1").Error)
	assert.Equal(t, "comprado", er.Status)

	// Idempotente: recibir de nuevo NO duplica el stock.
	w2 := doJSON(t, r, http.MethodPost, "/errands/er1/receive", nil)
	require.Equal(t, http.StatusOK, w2.Code)
	require.NoError(t, db.First(&ing, "id = ?", "arroz").Error)
	assert.Equal(t, 3.0, ing.Stock) // sigue en 3, no 6
}
