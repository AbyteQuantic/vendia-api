// Auditoría 2026-07-10 — tenant-scope y robustez del sync offline
// (POST /sync/batch). Tres defectos reales del motor:
//
//	(a) el camino `create` (syncEntity y syncProductWrite) buscaba la fila
//	    existente por id SIN filtrar tenant: como los UUID de producto son
//	    públicos (catálogo online), un cliente autenticado de OTRO tenant
//	    podía re-enviar un create con ese id y applyLWW sobreescribía la
//	    fila ajena — cross-tenant write (Art. III). applyLWW además
//	    aplicaba op.Data tal cual, permitiendo reescribir id/tenant_id.
//	(b) syncCreditPayment insertaba el abono sin verificar que la cuenta
//	    de fiado pertenezca al tenant del JWT → un tenant podía "abonar"
//	    (falsear) la deuda de la tienda de otro.
//	(c) una op create con data:null (cliente viejo/corrupto) hacía panic
//	    por asignación a map nil → 500 → el cliente reintenta el lote
//	    envenenado para siempre (el mismo patrón que ya rompió syncSale).
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

const (
	scopeTenantA = "aaaa0000-0000-4000-8000-000000000001"
	scopeTenantB = "bbbb0000-0000-4000-8000-000000000001"
)

func setupTenantScopeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Tenant{}, &models.Product{}, &models.Customer{},
		&models.CreditAccount{}, &models.CreditPayment{},
		&models.Sale{}, &models.SaleItem{}, &models.Event{},
	))
	for i, id := range []string{scopeTenantA, scopeTenantB} {
		require.NoError(t, db.Create(&models.Tenant{
			BaseModel: models.BaseModel{ID: id}, OwnerName: "Org",
			Phone: "300000000" + string(rune('0'+i)),
		}).Error)
	}
	return db
}

// (a) Un create re-enviado con el id de un producto de OTRO tenant jamás
// toca la fila ajena — ni sus campos ni su tenant_id.
func TestSync_CreateCannotOverwriteForeignTenantProduct(t *testing.T) {
	db := setupTenantScopeDB(t)
	foreign := models.Product{
		BaseModel: models.BaseModel{ID: "f0000000-0000-4000-8000-000000000001"},
		TenantID:  scopeTenantA, Name: "Cerveza Águila", Price: 3500, Barcode: "770001",
	}
	require.NoError(t, db.Create(&foreign).Error)

	svc := services.NewSyncService(db)
	_, err := svc.ProcessBatch(scopeTenantB, services.SyncRequest{
		Operations: []services.SyncOperation{{
			Entity: "product", Action: "create", ID: foreign.ID,
			ClientUpdatedAt: time.Now().Add(time.Hour), // gana cualquier LWW
			Data: map[string]any{
				"name": "HACKEADO", "price": 1, "tenant_id": scopeTenantB,
			},
		}},
	})
	require.NoError(t, err)

	var got models.Product
	require.NoError(t, db.First(&got, "id = ?", foreign.ID).Error)
	assert.Equal(t, "Cerveza Águila", got.Name, "la fila ajena no se toca")
	assert.Equal(t, scopeTenantA, got.TenantID, "el tenant_id jamás se roba")
	assert.Equal(t, float64(3500), got.Price)
}

// (a) Lo mismo para el camino genérico (customer) — el motor compartido.
func TestSync_CreateCannotOverwriteForeignTenantCustomer(t *testing.T) {
	db := setupTenantScopeDB(t)
	foreign := models.Customer{
		BaseModel: models.BaseModel{ID: "c0000000-0000-4000-8000-000000000001"},
		TenantID:  scopeTenantA, Name: "Doña Marta",
	}
	require.NoError(t, db.Create(&foreign).Error)

	svc := services.NewSyncService(db)
	_, err := svc.ProcessBatch(scopeTenantB, services.SyncRequest{
		Operations: []services.SyncOperation{{
			Entity: "customer", Action: "create", ID: foreign.ID,
			ClientUpdatedAt: time.Now().Add(time.Hour),
			Data:            map[string]any{"name": "HACKEADO"},
		}},
	})
	require.NoError(t, err)

	var got models.Customer
	require.NoError(t, db.First(&got, "id = ?", foreign.ID).Error)
	assert.Equal(t, "Doña Marta", got.Name)
	assert.Equal(t, scopeTenantA, got.TenantID)
}

// (a) Un update legítimo del PROPIO producto sigue aplicando, pero el payload
// no puede reescribir la identidad (tenant_id/id) de la fila.
func TestSync_UpdateCannotRewriteIdentityColumns(t *testing.T) {
	db := setupTenantScopeDB(t)
	own := models.Product{
		BaseModel: models.BaseModel{ID: "0b000000-0000-4000-8000-000000000001"},
		TenantID:  scopeTenantB, Name: "Pan", Price: 500,
	}
	require.NoError(t, db.Create(&own).Error)

	svc := services.NewSyncService(db)
	resp, err := svc.ProcessBatch(scopeTenantB, services.SyncRequest{
		Operations: []services.SyncOperation{{
			Entity: "product", Action: "update", ID: own.ID,
			ClientUpdatedAt: time.Now().Add(time.Hour),
			Data: map[string]any{
				"name": "Pan Aliñado", "tenant_id": scopeTenantA,
				"id": "99999999-9999-4999-8999-999999999999",
			},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, resp.Synced, "el update legítimo sí aplica")

	var got models.Product
	require.NoError(t, db.First(&got, "id = ?", own.ID).Error)
	assert.Equal(t, "Pan Aliñado", got.Name, "el campo legítimo se actualiza")
	assert.Equal(t, scopeTenantB, got.TenantID, "tenant_id no se reescribe")
}

// (a) syncJSONEntity (eventos): un create con el id de un evento ajeno no lo
// sobreescribe ni lo roba vía Save+SetIdentity.
func TestSync_JSONEntityCannotStealForeignEvent(t *testing.T) {
	db := setupTenantScopeDB(t)
	foreign := models.Event{Type: "curso", Title: "Curso de Panadería",
		Modality: "virtual", Status: "publicado"}
	foreign.SetIdentity("e0000000-0000-4000-8000-000000000009", scopeTenantA)
	require.NoError(t, db.Create(&foreign).Error)

	svc := services.NewSyncService(db)
	_, err := svc.ProcessBatch(scopeTenantB, services.SyncRequest{
		Operations: []services.SyncOperation{{
			Entity: "event", Action: "create", ID: foreign.ID,
			ClientUpdatedAt: time.Now().Add(time.Hour),
			Data:            map[string]any{"type": "otro", "title": "HACKEADO"},
		}},
	})
	require.NoError(t, err)

	var got models.Event
	require.NoError(t, db.First(&got, "id = ?", foreign.ID).Error)
	assert.Equal(t, "Curso de Panadería", got.Title)
	assert.Equal(t, scopeTenantA, got.TenantID)
}

// (b) Un abono sincronizado contra una cuenta de fiado de OTRO tenant no se
// inserta: falsearía la deuda de la otra tienda.
func TestSync_CreditPaymentForeignAccountIsRejected(t *testing.T) {
	db := setupTenantScopeDB(t)
	account := models.CreditAccount{
		BaseModel: models.BaseModel{ID: "ac000000-0000-4000-8000-000000000001"},
		TenantID:  scopeTenantA, CustomerID: "cf000000-0000-4000-8000-000000000001",
		TotalAmount: 50000,
	}
	require.NoError(t, db.Create(&account).Error)

	svc := services.NewSyncService(db)
	_, err := svc.ProcessBatch(scopeTenantB, services.SyncRequest{
		Operations: []services.SyncOperation{{
			Entity: "credit_payment", Action: "create",
			ID:              "0e000000-0000-4000-8000-000000000001",
			ClientUpdatedAt: time.Now(),
			Data: map[string]any{
				"credit_account_id": account.ID, "amount": 50000,
			},
		}},
	})
	require.NoError(t, err)

	var n int64
	require.NoError(t, db.Model(&models.CreditPayment{}).
		Where("credit_account_id = ?", account.ID).Count(&n).Error)
	assert.EqualValues(t, 0, n, "el abono ajeno jamás se inserta")
}

// (b) El abono legítimo del propio tenant sigue entrando (regresión).
func TestSync_CreditPaymentOwnAccountStillApplies(t *testing.T) {
	db := setupTenantScopeDB(t)
	account := models.CreditAccount{
		BaseModel: models.BaseModel{ID: "ac000000-0000-4000-8000-000000000002"},
		TenantID:  scopeTenantB, CustomerID: "cf000000-0000-4000-8000-000000000002",
		TotalAmount: 20000,
	}
	require.NoError(t, db.Create(&account).Error)

	svc := services.NewSyncService(db)
	resp, err := svc.ProcessBatch(scopeTenantB, services.SyncRequest{
		Operations: []services.SyncOperation{{
			Entity: "credit_payment", Action: "create",
			ID:              "0e000000-0000-4000-8000-000000000002",
			ClientUpdatedAt: time.Now(),
			Data: map[string]any{
				"credit_account_id": account.ID, "amount": 10000,
			},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, resp.Synced)

	var n int64
	require.NoError(t, db.Model(&models.CreditPayment{}).
		Where("credit_account_id = ?", account.ID).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}

// (c) Una op create con data:null no hace panic ni envenena el lote: la op
// siguiente del mismo lote se aplica normal.
func TestSync_NilDataCreateDoesNotPoisonBatch(t *testing.T) {
	db := setupTenantScopeDB(t)
	svc := services.NewSyncService(db)

	var resp *services.SyncResponse
	var err error
	require.NotPanics(t, func() {
		resp, err = svc.ProcessBatch(scopeTenantB, services.SyncRequest{
			Operations: []services.SyncOperation{
				{
					Entity: "product", Action: "create",
					ID:              "0d000000-0000-4000-8000-000000000001",
					ClientUpdatedAt: time.Now(),
					Data:            nil, // cliente viejo/corrupto
				},
				{
					Entity: "customer", Action: "create",
					ID:              "0d000000-0000-4000-8000-000000000002",
					ClientUpdatedAt: time.Now(),
					Data:            map[string]any{"name": "Cliente Válido"},
				},
			},
		})
	}, "data nil jamás puede tumbar el lote")
	require.NoError(t, err)
	require.NotNil(t, resp)

	var n int64
	require.NoError(t, db.Model(&models.Customer{}).
		Where("tenant_id = ?", scopeTenantB).Count(&n).Error)
	assert.EqualValues(t, 1, n, "la op válida del lote sobrevive a la corrupta")
}
