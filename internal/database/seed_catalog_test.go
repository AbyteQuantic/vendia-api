// Spec: specs/041-catalogo-dinamico-modulos-tipos/spec.md
package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"vendia-backend/internal/models"
)

func setupCatalogDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.BusinessModule{},
		&models.BusinessTypeCatalog{},
		&models.ModuleTypeRelation{},
	))
	return db
}

func TestSeedBusinessCatalog_SeedsParity(t *testing.T) {
	db := setupCatalogDB(t)

	n, err := SeedBusinessCatalog(db)
	require.NoError(t, err)
	assert.Equal(t, 15, n, "siembra los 15 módulos del dashboard")

	var types int64
	db.Model(&models.BusinessTypeCatalog{}).Count(&types)
	assert.Equal(t, int64(9), types, "siembra los 9 tipos de negocio")

	// El módulo core 'registrar_venta' existe y NO tiene capacidad.
	var venta models.BusinessModule
	require.NoError(t, db.Where("key = ?", "registrar_venta").First(&venta).Error)
	assert.Nil(t, venta.CapabilityKey)
	assert.Equal(t, models.RenderNative, venta.RenderType)

	// 'cotizaciones' mapea a enable_quotes y es 'sugerido' en ferretería.
	var quotes models.BusinessModule
	require.NoError(t, db.Where("key = ?", "cotizaciones").First(&quotes).Error)
	require.NotNil(t, quotes.CapabilityKey)
	assert.Equal(t, "enable_quotes", *quotes.CapabilityKey)

	var rel models.ModuleTypeRelation
	err = db.Where("module_id = ? AND business_type_value = ?", quotes.ID, models.BusinessTypeDepositoConstruccion).First(&rel).Error
	require.NoError(t, err)
	assert.Equal(t, models.RelationSuggested, rel.RelationLevel)
}

func TestSeedBusinessCatalog_Idempotent(t *testing.T) {
	db := setupCatalogDB(t)

	n1, err := SeedBusinessCatalog(db)
	require.NoError(t, err)
	assert.Equal(t, 15, n1)

	// Segunda corrida: no-op (no duplica).
	n2, err := SeedBusinessCatalog(db)
	require.NoError(t, err)
	assert.Equal(t, 0, n2)

	var modules int64
	db.Model(&models.BusinessModule{}).Count(&modules)
	assert.Equal(t, int64(15), modules, "no duplica en la segunda corrida")
}
