// Spec: specs/100-completar-skus-inventario/spec.md
//
// Spec 100 / D1 — dedup de barcode en el sync offline (POST /sync/batch).
// Dos defectos reales del camino `product` frente al índice único parcial
// idx_products_tenant_barcode_unique:
//
//	(a) create usaba ON CONFLICT DO NOTHING SIN target → la violación del
//	    índice de barcode se ABSORBÍA: el producto offline con código
//	    duplicado se descartaba en silencio (applied=true, fila inexistente)
//	    y el cliente quedaba desincronizado para siempre.
//	(b) update (applyLWW) no clasificaba la violación → el error tumbaba la
//	    transacción del lote COMPLETO → 500 → el cliente reintentaba el lote
//	    envenenado indefinidamente (mismo patrón del bug de syncSale,
//	    sync_service.go).
//
// Contrato esperado: barcode duplicado = conflicto NO aplicado (mismo
// tratamiento que un LWW perdido) — el lote SIEMPRE sigue, nunca silencio.
package services_test

import (
	"testing"
	"time"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupSyncBarcodeDB arma la BD del test con el MISMO índice único parcial
// que el bootstrap crea en prod (SQLite soporta índices parciales), para que
// el test reproduzca el comportamiento real del lote frente a la violación.
func setupSyncBarcodeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Tenant{}, &models.Product{}, &models.Sale{}, &models.SaleItem{},
		&models.Customer{}, &models.CreditAccount{},
	))
	require.NoError(t, db.Exec(
		`CREATE UNIQUE INDEX idx_products_tenant_barcode_unique
		 ON products (tenant_id, barcode)
		 WHERE barcode <> '' AND deleted_at IS NULL`,
	).Error)
	require.NoError(t, db.Create(&models.Tenant{
		BaseModel: models.BaseModel{ID: "tenant-a"}, OwnerName: "Org", Phone: "3000000000",
	}).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "0a000000-0000-4000-8000-000000000001"},
		TenantID:  "tenant-a", Name: "Coca-Cola", Barcode: "7701234567890", Price: 3000,
	}).Error)
	return db
}

// (a) Un create offline con barcode ya usado NO se aplica en silencio: se
// reporta como conflicto y el resto del lote sigue.
func TestSyncProduct_CreateDuplicateBarcode_ConflictNotSilent(t *testing.T) {
	db := setupSyncBarcodeDB(t)
	svc := services.NewSyncService(db)
	now := time.Now().UTC()

	resp, err := svc.ProcessBatch("tenant-a", services.SyncRequest{
		Operations: []services.SyncOperation{
			{
				Entity: "product", Action: "create",
				ID:              "0b000000-0000-4000-8000-000000000002",
				ClientUpdatedAt: now,
				Data: map[string]any{
					"name": "Coca-Cola Zero", "price": 3500, "stock": 4,
					"barcode": "7701234567890", // ya es de Coca-Cola
				},
			},
			{
				// Op válida encolada en el MISMO lote: debe aplicarse.
				Entity: "product", Action: "create",
				ID:              "0c000000-0000-4000-8000-000000000003",
				ClientUpdatedAt: now,
				Data:            map[string]any{"name": "Pony Malta", "price": 2500, "stock": 6},
			},
		},
	})

	require.NoError(t, err, "el lote nunca debe caerse por un barcode duplicado")
	assert.Equal(t, 1, resp.Conflicts, "el duplicado debe reportarse como conflicto, no en silencio")
	assert.Equal(t, 1, resp.Synced, "la op válida del lote debe aplicarse")

	var count int64
	db.Model(&models.Product{}).Where("id = ?", "0b000000-0000-4000-8000-000000000002").Count(&count)
	assert.EqualValues(t, 0, count, "el producto con barcode duplicado no debe crearse")
	db.Model(&models.Product{}).Where("id = ?", "0c000000-0000-4000-8000-000000000003").Count(&count)
	assert.EqualValues(t, 1, count)
}

// (b) Un update offline con barcode ya usado NO tumba el lote completo: la
// op queda en conflicto, el producto conserva su código y las demás ops del
// lote se aplican.
func TestSyncProduct_UpdateDuplicateBarcode_BatchSurvives(t *testing.T) {
	db := setupSyncBarcodeDB(t)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "0d000000-0000-4000-8000-000000000004"},
		TenantID:  "tenant-a", Name: "Pony Malta", Barcode: "", Price: 2500,
	}).Error)
	svc := services.NewSyncService(db)
	future := time.Now().UTC().Add(time.Hour) // gana el LWW: el conflicto es SOLO por barcode

	resp, err := svc.ProcessBatch("tenant-a", services.SyncRequest{
		Operations: []services.SyncOperation{
			{
				Entity: "product", Action: "update",
				ID:              "0d000000-0000-4000-8000-000000000004",
				ClientUpdatedAt: future,
				Data:            map[string]any{"barcode": "7701234567890", "price": 2800},
			},
			{
				// Op válida detrás de la envenenada: NO debe perderse por rollback.
				Entity: "product", Action: "create",
				ID:              "0e000000-0000-4000-8000-000000000005",
				ClientUpdatedAt: future,
				Data:            map[string]any{"name": "Chocorramo", "price": 2000, "stock": 10},
			},
		},
	})

	require.NoError(t, err, "el lote completo no debe caerse (500) por la violación del índice")
	assert.Equal(t, 1, resp.Conflicts)
	assert.Equal(t, 1, resp.Synced)

	var updated models.Product
	require.NoError(t, db.First(&updated, "id = ?", "0d000000-0000-4000-8000-000000000004").Error)
	assert.Equal(t, "", updated.Barcode, "el barcode en conflicto no debe asignarse")

	var count int64
	db.Model(&models.Product{}).Where("id = ?", "0e000000-0000-4000-8000-000000000005").Count(&count)
	assert.EqualValues(t, 1, count, "la op válida del lote debe aplicarse")
}
