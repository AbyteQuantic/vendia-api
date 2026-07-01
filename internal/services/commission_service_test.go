// Spec: specs/084-peluqueria-salon/spec.md
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

func setupCommissionDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.EmployeePayConfig{}))
	return db
}

func pctPtr(v float64) *float64 { return &v }

func seedPayConfig(t *testing.T, db *gorm.DB, cfg models.EmployeePayConfig) {
	t.Helper()
	require.NoError(t, db.Create(&cfg).Error)
}

// TestCommissionResolver_ReturnsActiveConfigForTenantAndEmployee asegura que el
// resolver devuelve la config activa del profesional filtrando por tenant.
func TestCommissionResolver_ReturnsActiveConfigForTenantAndEmployee(t *testing.T) {
	db := setupCommissionDB(t)
	const tenant, emp = "t1", "e1"
	seedPayConfig(t, db, models.EmployeePayConfig{
		TenantID: tenant, EmployeeUUID: emp, PayModel: models.PayModelCommission,
		CommissionPct: pctPtr(10), EffectiveFrom: time.Now().Add(-24 * time.Hour), IsActive: true,
	})

	r := services.NewCommissionResolver(db, tenant)
	got := r.Config(emp)
	require.NotNil(t, got)
	assert.Equal(t, models.PayModelCommission, got.PayModel)
	require.NotNil(t, got.CommissionPct)
	assert.Equal(t, 10.0, *got.CommissionPct)
}

// TestCommissionResolver_PicksMostRecentEffectiveFrom — ante varias configs
// activas, gana la de effective_from más reciente (Order DESC).
func TestCommissionResolver_PicksMostRecentEffectiveFrom(t *testing.T) {
	db := setupCommissionDB(t)
	const tenant, emp = "t1", "e1"
	seedPayConfig(t, db, models.EmployeePayConfig{
		TenantID: tenant, EmployeeUUID: emp, PayModel: models.PayModelCommission,
		CommissionPct: pctPtr(5), EffectiveFrom: time.Now().Add(-72 * time.Hour), IsActive: true,
	})
	seedPayConfig(t, db, models.EmployeePayConfig{
		TenantID: tenant, EmployeeUUID: emp, PayModel: models.PayModelCommission,
		CommissionPct: pctPtr(15), EffectiveFrom: time.Now().Add(-1 * time.Hour), IsActive: true,
	})

	got := services.NewCommissionResolver(db, tenant).Config(emp)
	require.NotNil(t, got)
	require.NotNil(t, got.CommissionPct)
	assert.Equal(t, 15.0, *got.CommissionPct)
}

// TestCommissionResolver_IgnoresInactiveAndOtherTenant — no filtra inactivas ni
// configs de otro tenant (aislamiento Art. III).
func TestCommissionResolver_IgnoresInactiveAndOtherTenant(t *testing.T) {
	db := setupCommissionDB(t)
	const tenant, emp = "t1", "e1"
	// IsActive=false debe persistirse explícitamente: GORM omite el zero-value
	// `false` cuando la columna tiene default:true, así que forzamos el flag con
	// un Update por mapa tras el insert.
	inactive := models.EmployeePayConfig{
		TenantID: tenant, EmployeeUUID: emp, PayModel: models.PayModelCommission,
		CommissionPct: pctPtr(20), EffectiveFrom: time.Now(), IsActive: true,
	}
	require.NoError(t, db.Create(&inactive).Error)
	require.NoError(t, db.Model(&inactive).Update("is_active", false).Error)

	seedPayConfig(t, db, models.EmployeePayConfig{
		TenantID: "otro", EmployeeUUID: emp, PayModel: models.PayModelCommission,
		CommissionPct: pctPtr(30), EffectiveFrom: time.Now(), IsActive: true,
	})

	assert.Nil(t, services.NewCommissionResolver(db, tenant).Config(emp))
}

// TestCommissionResolver_CachesNilAndHit — la segunda llamada para el mismo
// empleado no vuelve a la BD (caché), incluido el caso "sin config" (nil).
func TestCommissionResolver_CachesNilAndHit(t *testing.T) {
	db := setupCommissionDB(t)
	const tenant, emp = "t1", "e1"
	r := services.NewCommissionResolver(db, tenant)

	assert.Nil(t, r.Config(emp)) // miss cacheado como nil

	// Ahora inserto una config: si la caché funciona, el resolver sigue nil.
	seedPayConfig(t, db, models.EmployeePayConfig{
		TenantID: tenant, EmployeeUUID: emp, PayModel: models.PayModelCommission,
		CommissionPct: pctPtr(10), EffectiveFrom: time.Now(), IsActive: true,
	})
	assert.Nil(t, r.Config(emp), "debe devolver el nil cacheado, no reconsultar")

	// Un resolver nuevo (nueva operación) sí ve la config.
	got := services.NewCommissionResolver(db, tenant).Config(emp)
	require.NotNil(t, got)
}

// TestRecomputeCommissionOnNetOfTax_NoOpWhenExempt — tax<=0 no muta nada.
func TestRecomputeCommissionOnNetOfTax_NoOpWhenExempt(t *testing.T) {
	items := []models.SaleItem{
		{Subtotal: 10000, PayBasis: models.PayBasisCommission, CommissionPct: pctPtr(10), CommissionAmount: 1000},
	}
	services.RecomputeCommissionOnNetOfTax(items, 0)
	assert.Equal(t, 1000.0, items[0].CommissionAmount)
}

// TestRecomputeCommissionOnNetOfTax_RecalculatesOnNet — con IVA, la comisión de
// una línea commission se recalcula sobre el net (subtotal − IVA prorrateado),
// preservando el redondeo exacto del bloque original.
func TestRecomputeCommissionOnNetOfTax_RecalculatesOnNet(t *testing.T) {
	items := []models.SaleItem{
		{Subtotal: 10000, PayBasis: models.PayBasisCommission, CommissionPct: pctPtr(10), CommissionAmount: 1000},
	}
	// Única línea → todo el IVA cae en ella. net = 10000 - 1900 = 8100.
	// comisión = round_half_up(8100 * 10 / 100) = 810.
	services.RecomputeCommissionOnNetOfTax(items, 1900)
	assert.Equal(t, 810.0, items[0].CommissionAmount)
}

// TestRecomputeCommissionOnNetOfTax_LeavesNonCommissionUntouched — solo toca las
// líneas commission con pct no-nil; el resto queda intacto.
func TestRecomputeCommissionOnNetOfTax_LeavesNonCommissionUntouched(t *testing.T) {
	items := []models.SaleItem{
		{Subtotal: 5000, PayBasis: models.PayBasisFixed, CommissionAmount: 0},
		{Subtotal: 5000, PayBasis: models.PayBasisCommission, CommissionPct: nil, CommissionAmount: 0},
	}
	services.RecomputeCommissionOnNetOfTax(items, 1000)
	assert.Equal(t, 0.0, items[0].CommissionAmount)
	assert.Equal(t, 0.0, items[1].CommissionAmount)
}
