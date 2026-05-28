// Spec: specs/038-push-notifications-web-android/spec.md
package push

import (
	"context"
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedTenantWithThreshold(t *testing.T, db interface {
	Create(value any, conds ...any) any
}, _ string, _ *int) {
	// no-op — usamos setupDispatcherDB y seed manual abajo
}

// T-11a-1 — Producto con stock por encima del umbral NO dispara push.
func TestCheckStockLow_AboveThresholdNoDispatch(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantID := "aaaaaaaa-1111-1111-1111-111111111111"
	seedActiveTenant(t, db, tenantID)
	seedToken(t, db, tenantID, "u", "tok")
	// Threshold por defecto = 3. Stock = 5 → no push.
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "prod-1"},
		TenantID:  tenantID, Name: "Coca", Stock: 5,
	}).Error)

	fake := &FakeSender{}
	d := newDispatcherWith(fake, time.Now())

	CheckStockLow(context.Background(), db, d, tenantID, []string{"prod-1"})
	assert.Empty(t, fake.Calls)
}

// T-11a-2 — Producto con stock <= umbral SÍ dispara push.
func TestCheckStockLow_AtOrBelowThresholdDispatches(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantID := "aaaaaaaa-2222-2222-2222-222222222222"
	seedActiveTenant(t, db, tenantID)
	seedToken(t, db, tenantID, "u", "tok")
	// Threshold por defecto = 3. Stock = 2 → push.
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "prod-2"},
		TenantID:  tenantID, Name: "Galletas", Stock: 2,
	}).Error)

	fake := &FakeSender{}
	d := newDispatcherWith(fake, time.Now())

	CheckStockLow(context.Background(), db, d, tenantID, []string{"prod-2"})
	require.Len(t, fake.Calls, 1)
	assert.Contains(t, fake.Calls[0].Payload.Title, "Stock bajo")
	assert.Contains(t, fake.Calls[0].Payload.Title, "Galletas")
}

// T-11a-3 — AC-08: vender el mismo producto 2 veces el mismo día solo
// envía UNA push (el dedup_key incluye la fecha).
func TestCheckStockLow_OncePerProductPerDay(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantID := "aaaaaaaa-3333-3333-3333-333333333333"
	seedActiveTenant(t, db, tenantID)
	seedToken(t, db, tenantID, "u", "tok")
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "prod-3"},
		TenantID:  tenantID, Name: "Arroz", Stock: 1,
	}).Error)

	fake := &FakeSender{}
	d := newDispatcherWith(fake, time.Now())

	CheckStockLow(context.Background(), db, d, tenantID, []string{"prod-3"})
	CheckStockLow(context.Background(), db, d, tenantID, []string{"prod-3"})
	assert.Len(t, fake.Calls, 1, "segundo chequeo en el mismo día NO duplica push")
}

// T-11a-4 — Threshold custom del tenant tiene precedencia sobre default.
func TestCheckStockLow_RespectsCustomThreshold(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantID := "aaaaaaaa-4444-4444-4444-444444444444"
	seedActiveTenant(t, db, tenantID)
	seedToken(t, db, tenantID, "u", "tok")
	// Threshold custom = 10. Stock = 8 → push (8 ≤ 10).
	customThreshold := 10
	require.NoError(t, db.Model(&models.Tenant{}).
		Where("id = ?", tenantID).
		Update("stock_low_threshold", &customThreshold).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "prod-4"},
		TenantID:  tenantID, Name: "Aceite", Stock: 8,
	}).Error)

	fake := &FakeSender{}
	d := newDispatcherWith(fake, time.Now())

	CheckStockLow(context.Background(), db, d, tenantID, []string{"prod-4"})
	require.Len(t, fake.Calls, 1)
}

// T-11a-5 — Threshold = 0 (tenant deshabilitó el chequeo) → no push.
func TestCheckStockLow_DisabledByZeroThreshold(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantID := "aaaaaaaa-5555-5555-5555-555555555555"
	seedActiveTenant(t, db, tenantID)
	seedToken(t, db, tenantID, "u", "tok")
	zero := 0
	require.NoError(t, db.Model(&models.Tenant{}).
		Where("id = ?", tenantID).
		Update("stock_low_threshold", &zero).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "prod-5"},
		TenantID:  tenantID, Name: "Pan", Stock: 1,
	}).Error)

	fake := &FakeSender{}
	d := newDispatcherWith(fake, time.Now())

	CheckStockLow(context.Background(), db, d, tenantID, []string{"prod-5"})
	assert.Empty(t, fake.Calls, "threshold=0 deshabilita el chequeo")
}

// T-11a-6 — Aislamiento cross-tenant: stock bajo del tenant A no
// dispara push en tenant B (defense-in-depth Art. III).
func TestCheckStockLow_DoesNotLeakAcrossTenants(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantA := "aaaaaaaa-6666-6666-6666-666666666666"
	tenantB := "bbbbbbbb-6666-6666-6666-666666666666"
	seedActiveTenant(t, db, tenantA)
	seedActiveTenant(t, db, tenantB)
	seedToken(t, db, tenantB, "u-b", "tok-b")
	// Producto del tenant B con stock bajo:
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "prod-B"},
		TenantID:  tenantB, Name: "Stock bajo de B", Stock: 1,
	}).Error)

	fake := &FakeSender{}
	d := newDispatcherWith(fake, time.Now())

	// Llamada con tenantID=A pero pasando un product_id que pertenece a B.
	// El query filtra por (tenant_id=A AND id IN [prod-B]) → 0 productos.
	CheckStockLow(context.Background(), db, d, tenantA, []string{"prod-B"})
	assert.Empty(t, fake.Calls, "tenant A NO debe ver productos de tenant B")
}

// T-11a-7 — Dispatcher nil = no-op (degradación graceful).
func TestCheckStockLow_NilDispatcherIsNoop(t *testing.T) {
	db := setupDispatcherDB(t)
	// Sin crash:
	CheckStockLow(context.Background(), db, nil, "any", []string{"any"})
}
