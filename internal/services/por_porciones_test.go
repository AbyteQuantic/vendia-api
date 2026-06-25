// Spec: specs/080-platos-por-porciones/spec.md
package services_test

import (
	"testing"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func markPorPorciones(t *testing.T, db *gorm.DB, productID string, stock int) {
	t.Helper()
	require.NoError(t, db.Model(&models.Product{}).
		Where("id = ?", productID).
		Updates(map[string]any{"availability_mode": "por_porciones", "stock": stock}).Error)
}

// Spec 080 AC-02: vender un plato por_porciones NO explota insumos (ya se
// descontaron al cocinar el lote). ExplodeRecipe es no-op en la venta.
func TestExplodeRecipe_PorPorciones_NoOpOnSale(t *testing.T) {
	db := setupRecipeDB(t)
	f := seedAlmuerzoCorriente(t, db)
	markPorPorciones(t, db, f.productID, 10)

	svc := services.NewRecipeService(db)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return svc.ExplodeRecipe(tx, services.ExplodeParams{
			TenantID: f.tenantID, SaleUUID: "sale-x",
			ProductID: f.productID, Quantity: 2, // ForPrep: false (venta)
		})
	}))

	var arroz, pollo models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	require.NoError(t, db.First(&pollo, "id = ?", f.polloID).Error)
	assert.InDelta(t, 3.0, arroz.Stock, 1e-9, "insumos intactos en venta por_porciones")
	assert.InDelta(t, 2.0, pollo.Stock, 1e-9, "insumos intactos en venta por_porciones")
}

// Spec 080 AC-01: cocinar el lote (ForPrep=true) SÍ explota insumos aunque el
// plato sea por_porciones — es el ÚNICO momento en que se descuentan.
func TestExplodeRecipe_PorPorciones_ForPrep_DiscountsInsumos(t *testing.T) {
	db := setupRecipeDB(t)
	f := seedAlmuerzoCorriente(t, db)
	markPorPorciones(t, db, f.productID, 0)

	svc := services.NewRecipeService(db)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return svc.ExplodeRecipe(tx, services.ExplodeParams{
			TenantID: f.tenantID, SaleUUID: "batch:p:2026-06-25",
			ProductID: f.productID, Quantity: 10, ForPrep: true,
		})
	}))

	var arroz, pollo models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	require.NoError(t, db.First(&pollo, "id = ?", f.polloID).Error)
	assert.InDelta(t, 1.5, arroz.Stock, 1e-9, "arroz 3 - 10*0.15 = 1.5")
	assert.InDelta(t, 0.0, pollo.Stock, 1e-9, "pollo 2 - 10*0.20 = 0")
}

// Spec 080 AC-02: ApplyPostSale de un plato por_porciones descuenta STOCK
// (porciones restantes), NO insumos → cero doble descuento.
func TestApplyPostSale_PorPorciones_DecrementsStockNotInsumos(t *testing.T) {
	db := setupRecipeDB(t)
	f := seedAlmuerzoCorriente(t, db)
	markPorPorciones(t, db, f.productID, 10)

	svc := services.NewSaleInventoryService(db)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return svc.ApplyPostSale(tx, services.PostSaleParams{
			TenantID: f.tenantID, SaleUUID: "sale-y",
			Lines: []services.SaleInventoryLine{{ProductID: f.productID, Quantity: 2}},
		})
	}))

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", f.productID).Error)
	assert.Equal(t, 8, prod.Stock, "10 - 2 porciones vendidas")

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 3.0, arroz.Stock, 1e-9, "insumos NO se tocan en la venta por_porciones")
}

// Spec 080 AC-04: un plato a_demanda (default) sigue explotando insumos en la
// venta — comportamiento intacto (retrocompatible).
func TestApplyPostSale_ADemanda_StillExplodes(t *testing.T) {
	db := setupRecipeDB(t)
	f := seedAlmuerzoCorriente(t, db) // sin marcar → a_demanda

	svc := services.NewSaleInventoryService(db)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return svc.ApplyPostSale(tx, services.PostSaleParams{
			TenantID: f.tenantID, SaleUUID: "sale-z",
			Lines: []services.SaleInventoryLine{{ProductID: f.productID, Quantity: 2}},
		})
	}))

	var arroz models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	assert.InDelta(t, 2.70, arroz.Stock, 1e-9, "a_demanda: 3 - 2*0.15 = 2.70 (explota)")
}
