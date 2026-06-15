// Spec: specs/047-offline-sync-contract/spec.md
//
// Regresión: una operación offline `entity:"sale"` encolada por clientes
// (jsonData = localSale.toJson(): llaves uuid/customer_uuid/is_credit_sale/
// items que NO son columnas de `sales`) hacía que syncEntity.Create(map)
// fallara, abortando TODA la transacción del lote en ProcessBatch. Resultado
// real (replay contra PROD): HTTP 500 y, peor, las ops de producto/cliente
// encoladas en el MISMO lote se perdían por el rollback, reintentando para
// siempre. Las ventas ahora viajan SOLO por POST /api/v1/sales (idempotente);
// el camino /sync/batch para 'sale' debe ack-and-skip para drenar las colas
// rotas ya instaladas sin envenenar el lote.
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

func setupSaleSkipDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Tenant{}, &models.Product{}, &models.Sale{}, &models.SaleItem{},
	))
	require.NoError(t, db.Create(&models.Tenant{
		BaseModel: models.BaseModel{ID: "tenant-a"}, OwnerName: "Org", Phone: "3000000000",
	}).Error)
	return db
}

// A legacy offline 'sale' op MUST NOT abort the batch nor roll back the
// co-queued product create. It is ack'd (skip) so the client drains it.
func TestSync_LegacySaleOp_DoesNotPoisonBatch(t *testing.T) {
	db := setupSaleSkipDB(t)
	svc := services.NewSyncService(db)
	now := time.Now().UTC()

	resp, err := svc.ProcessBatch("tenant-a", services.SyncRequest{
		Operations: []services.SyncOperation{
			{
				// EXACT shape of localSale.toJson() — keys are NOT sales columns.
				Entity: "sale", Action: "create",
				ID:              "5e000000-0000-4000-8000-000000000001",
				ClientUpdatedAt: now,
				Data: map[string]any{
					"uuid":           "5e000000-0000-4000-8000-000000000001",
					"total":          1600,
					"payment_method": "cash",
					"customer_uuid":  nil,
					"is_credit_sale": false,
					"sale_origin":    "counter",
					"table_label":    nil,
					"items":          []any{map[string]any{"product_uuid": "p1", "quantity": 1, "unit_price": 1600}},
					"created_at":     now.Format(time.RFC3339),
				},
			},
			{
				// A perfectly valid product create queued in the SAME batch.
				Entity: "product", Action: "create",
				ID:              "9b000000-0000-4000-8000-000000000002",
				ClientUpdatedAt: now,
				Data: map[string]any{
					"name": "Producto Coqueueado", "price": 5000, "stock": 7,
				},
			},
		},
	})

	// The batch must succeed (no 500) ...
	require.NoError(t, err)

	// ... the co-queued product MUST commit (not rolled back) ...
	var products int64
	require.NoError(t, db.Model(&models.Product{}).
		Where("id = ? AND tenant_id = ?", "9b000000-0000-4000-8000-000000000002", "tenant-a").
		Count(&products).Error)
	assert.Equal(t, int64(1), products, "co-queued product must commit despite the legacy sale op")

	// ... and the malformed sale must NOT have created a bogus row.
	var sales int64
	require.NoError(t, db.Model(&models.Sale{}).Count(&sales).Error)
	assert.Equal(t, int64(0), sales, "legacy sale op must be skipped, not inserted")

	// The sale op is counted as applied/ack'd so the client deletes it
	// from its queue (self-drain). Both ops accounted for.
	assert.Equal(t, 2, resp.Synced, "both ops ack'd (sale skipped, product created)")
}
