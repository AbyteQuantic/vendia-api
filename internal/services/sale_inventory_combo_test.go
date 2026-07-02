// Pregunta real del fundador: al vender un combo (Promotion + PromotionItem,
// Spec del concilio de branch-scope en combos), ¿el stock se descuenta de la
// sede correcta y queda el kardex correcto? La investigación del concilio
// confirmó por LECTURA de código que no hay bug (sales.go no tiene ninguna
// lógica especial para combos — el POS los descompone en líneas de producto
// normales antes de llamar a CreateSale, y ese camino genérico SÍ está bien
// scopeado por sucursal). Este archivo convierte esa lectura de código en una
// prueba real: crea un combo scopeado a una sede, "lo vende" armando las
// líneas exactamente como lo haría el POS, y verifica stock + kardex.
package services

import (
	"testing"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestApplyPostSale_ComboSale_DecrementsCorrectBranchAndLogsKardex reproduce
// el flujo real: un combo "2x1 Gaseosa + Pan" creado para Sede Norte, con
// copias del mismo producto en Sede Norte y Sede Sur (mismo patrón que
// TestCreateSale_BranchIsolation_StockDecrementStaysInSelectedBranch en
// branch_isolation_test.go). Vender el combo en Sede Norte debe:
//  1. Descontar SOLO el stock de Sede Norte (Sede Sur queda intacta).
//  2. Registrar un inventory_movement de tipo "sale" POR CADA producto del
//     combo, con branch_id = Sede Norte.
func TestApplyPostSale_ComboSale_DecrementsCorrectBranchAndLogsKardex(t *testing.T) {
	db := setupSaleInventoryDB(t)
	require.NoError(t, db.AutoMigrate(&models.Promotion{}, &models.PromotionItem{}))

	tenantID := "tenant-combo-branch"
	sedeNorte := "b0000000-0000-4000-8000-00000000a001"
	sedeSur := "b0000000-0000-4000-8000-00000000a002"

	gaseosaNorteID := "c0000000-0000-4000-8000-00000000a001"
	gaseosaSurID := "c0000000-0000-4000-8000-00000000a002"
	panNorteID := "c0000000-0000-4000-8000-00000000a003"
	panSurID := "c0000000-0000-4000-8000-00000000a004"

	// Cada sede tiene su propia copia física del mismo producto lógico —
	// mismo patrón que products.go usa para scoping real por sucursal.
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: gaseosaNorteID},
		TenantID:  tenantID, BranchID: &sedeNorte,
		Name: "Gaseosa Cola", Price: 2500, Stock: 20, IsAvailable: true,
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: gaseosaSurID},
		TenantID:  tenantID, BranchID: &sedeSur,
		Name: "Gaseosa Cola", Price: 2500, Stock: 15, IsAvailable: true,
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: panNorteID},
		TenantID:  tenantID, BranchID: &sedeNorte,
		Name: "Pan", Price: 1500, Stock: 30, IsAvailable: true,
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: panSurID},
		TenantID:  tenantID, BranchID: &sedeSur,
		Name: "Pan", Price: 1500, Stock: 25, IsAvailable: true,
	}).Error)

	// El combo: "Gaseosa + Pan" scopeado a Sede Norte (fix del concilio —
	// Promotion.BranchID). Los PromotionItem apuntan a las copias de Sede
	// Norte porque ese combo solo tiene sentido/stock en esa sede.
	promoID := "d0000000-0000-4000-8000-00000000a001"
	require.NoError(t, db.Create(&models.Promotion{
		BaseModel: models.BaseModel{ID: promoID},
		TenantID:  tenantID, BranchID: &sedeNorte,
		Name: "Combo Gaseosa + Pan", PromoType: "combo", IsActive: true,
		Items: []models.PromotionItem{
			{PromotionID: promoID, ProductID: gaseosaNorteID, Quantity: 1, PromoPrice: 2000},
			{PromotionID: promoID, ProductID: panNorteID, Quantity: 2, PromoPrice: 1200},
		},
	}).Error)

	// "Vender" el combo: el POS lo descompone en líneas de producto
	// normales (mismo contrato que sales.go — sin lógica especial de
	// promoción, confirmado por la investigación del concilio).
	var promo models.Promotion
	require.NoError(t, db.Preload("Items").First(&promo, "id = ?", promoID).Error)
	require.Len(t, promo.Items, 2)

	lines := make([]SaleInventoryLine, len(promo.Items))
	for i, item := range promo.Items {
		lines[i] = SaleInventoryLine{ProductID: item.ProductID, Quantity: item.Quantity}
	}

	svc := NewSaleInventoryService(db)
	saleUUID := "e0000000-0000-4000-8000-00000000a001"
	err := db.Transaction(func(tx *gorm.DB) error {
		return svc.ApplyPostSale(tx, PostSaleParams{
			TenantID: tenantID, BranchID: &sedeNorte,
			SaleUUID: saleUUID,
			Lines:    lines,
		})
	})
	require.NoError(t, err)

	// 1. Stock de Sede Norte descontado (20-1=19, 30-2=28).
	var gaseosaNorte, panNorte models.Product
	require.NoError(t, db.First(&gaseosaNorte, "id = ?", gaseosaNorteID).Error)
	require.NoError(t, db.First(&panNorte, "id = ?", panNorteID).Error)
	assert.Equal(t, 19, gaseosaNorte.Stock, "Sede Norte: 20 - 1 = 19")
	assert.Equal(t, 28, panNorte.Stock, "Sede Norte: 30 - 2 = 28")

	// 2. Stock de Sede Sur INTACTO — el combo de Sede Norte nunca debe
	// tocar el inventario de otra sede.
	var gaseosaSur, panSur models.Product
	require.NoError(t, db.First(&gaseosaSur, "id = ?", gaseosaSurID).Error)
	require.NoError(t, db.First(&panSur, "id = ?", panSurID).Error)
	assert.Equal(t, 15, gaseosaSur.Stock, "Sede Sur no debe verse afectada por el combo de Sede Norte")
	assert.Equal(t, 25, panSur.Stock, "Sede Sur no debe verse afectada por el combo de Sede Norte")

	// 3. Kardex: un movimiento "sale" por cada producto del combo, con
	// branch_id = Sede Norte.
	var movements []models.InventoryMovement
	require.NoError(t, db.Where("movement_type = ?", models.MovementSale).
		Order("product_id").Find(&movements).Error)
	require.Len(t, movements, 2, "un movimiento de kardex por cada producto del combo")
	for _, mov := range movements {
		require.NotNil(t, mov.BranchID, "el movimiento de kardex debe llevar la sede")
		assert.Equal(t, sedeNorte, *mov.BranchID,
			"el kardex del combo debe quedar atado a la sede donde se vendió")
	}
	// Verifica las cantidades exactas por producto (negativo = salida).
	byProduct := map[string]models.InventoryMovement{}
	for _, mov := range movements {
		byProduct[mov.ProductID] = mov
	}
	assert.Equal(t, -1.0, byProduct[gaseosaNorteID].Quantity, "1 gaseosa vendida")
	assert.Equal(t, -2.0, byProduct[panNorteID].Quantity, "2 panes vendidos")
}
