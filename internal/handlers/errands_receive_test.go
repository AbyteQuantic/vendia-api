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

// Spec 078 B2 — una línea de PRODUCTO de tienda ingresa al stock del producto.
func TestReceiveErrand_ProductLine_EntersProductStock(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Ingredient{}, &models.Product{}, &models.PurchaseErrand{}, &models.PurchaseErrandLine{}, &models.InventoryMovement{}))
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "gas"}, TenantID: "t1", Name: "Gaseosa", Stock: 1, PurchasePrice: 0}).Error)
	require.NoError(t, db.Create(&models.PurchaseErrand{BaseModel: models.BaseModel{ID: "e2"}, TenantID: "t1", Status: "pendiente"}).Error)
	pid := "gas"
	require.NoError(t, db.Create(&models.PurchaseErrandLine{BaseModel: models.BaseModel{ID: "lp"}, ErrandID: "e2", LineKind: "product", ProductID: &pid, Name: "Gaseosa", Qty: 12, EstimatedUnitPrice: 2000}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.POST("/errands/:id/receive", handlers.ReceiveErrand(db))
	w := doJSON(t, r, http.MethodPost, "/errands/e2/receive", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var p models.Product
	require.NoError(t, db.First(&p, "id = ?", "gas").Error)
	assert.Equal(t, 13, p.Stock)             // 1 + 12
	assert.Equal(t, 2000.0, p.PurchasePrice) // costo real
}

// Spec 078 B3 — compra PARCIAL idempotente por delta (recibir 2 de 5, luego el resto).
func TestReceiveErrand_Partial_NoDoubleCount(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Ingredient{}, &models.Product{}, &models.PurchaseErrand{}, &models.PurchaseErrandLine{}, &models.InventoryMovement{}))
	require.NoError(t, db.Create(&models.Ingredient{BaseModel: models.BaseModel{ID: "arroz"}, TenantID: "t1", Name: "Arroz", Unit: "kg", Stock: 0}).Error)
	require.NoError(t, db.Create(&models.PurchaseErrand{BaseModel: models.BaseModel{ID: "e3"}, TenantID: "t1", Status: "pendiente"}).Error)
	iid := "arroz"
	require.NoError(t, db.Create(&models.PurchaseErrandLine{BaseModel: models.BaseModel{ID: "l3"}, ErrandID: "e3", IngredientID: &iid, LineKind: "ingredient", Name: "Arroz", Unit: "kg", Qty: 5, EstimatedUnitPrice: 2800}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.POST("/errands/:id/receive", handlers.ReceiveErrand(db))

	// Recibe 2 de 5 → stock 2, mandado PARCIAL.
	w1 := doJSON(t, r, http.MethodPost, "/errands/e3/receive", map[string]any{"lines": []map[string]any{{"line_id": "l3", "received_qty": 2}}})
	require.Equal(t, http.StatusOK, w1.Code)
	var ing models.Ingredient
	require.NoError(t, db.First(&ing, "id = ?", "arroz").Error)
	assert.Equal(t, 2.0, ing.Stock)
	var er models.PurchaseErrand
	require.NoError(t, db.First(&er, "id = ?", "e3").Error)
	assert.Equal(t, "parcial", er.Status)

	// Recibe el total 5 → ingresa solo el delta 3 → stock 5, COMPRADO, sin doble conteo.
	w2 := doJSON(t, r, http.MethodPost, "/errands/e3/receive", map[string]any{"lines": []map[string]any{{"line_id": "l3", "received_qty": 5}}})
	require.Equal(t, http.StatusOK, w2.Code)
	require.NoError(t, db.First(&ing, "id = ?", "arroz").Error)
	assert.Equal(t, 5.0, ing.Stock) // 2 + 3, no 2+5
	require.NoError(t, db.First(&er, "id = ?", "e3").Error)
	assert.Equal(t, "comprado", er.Status)
}
