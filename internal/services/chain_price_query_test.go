// Spec: specs/077-compra-inteligente-insumos/spec.md
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

func setupChainDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.ChainPrice{}))
	return db
}

func mkCP(t *testing.T, db *gorm.DB, chain, name string, price float64, daysAgo int) {
	t.Helper()
	require.NoError(t, db.Create(&models.ChainPrice{
		Chain: chain, City: "Fusagasugá", RawName: name,
		NormalizedName: services.NormalizeText(name), Price: price,
		Unit: "kg", ScrapedAt: time.Now().AddDate(0, 0, -daysAgo),
	}).Error)
}

func TestMatchChainPrices_DetectsDrop(t *testing.T) {
	db := setupChainDB(t)
	// D1 arroz: base ~3200 (20d, 10d atrás) y hoy 2700 → bajó ~16%.
	mkCP(t, db, "exito", "Arroz Diana 1 Kg", 3200, 20)
	mkCP(t, db, "exito", "Arroz Diana 1 Kg", 3200, 10)
	mkCP(t, db, "exito", "Arroz Diana 1 Kg", 2700, 0)

	matches := services.MatchChainPrices(db, services.NormalizeText("arroz"), "Fusagasugá")
	require.Len(t, matches, 1)
	m := matches[0]
	assert.Equal(t, "exito", m.Chain)
	assert.InDelta(t, 2700, m.Price, 0.001) // último
	assert.True(t, m.Dropped)               // bajó ≥10%
	assert.InDelta(t, 16.0, m.DropPct, 1.5) // ~16%
}

func TestMatchChainPrices_NoDropWhenStable(t *testing.T) {
	db := setupChainDB(t)
	mkCP(t, db, "olimpica", "Aceite Premier 900 ml", 9000, 15)
	mkCP(t, db, "olimpica", "Aceite Premier 900 ml", 9100, 0)
	matches := services.MatchChainPrices(db, services.NormalizeText("aceite"), "Fusagasugá")
	require.Len(t, matches, 1)
	assert.False(t, matches[0].Dropped)
}

func TestPurgeOldChainPrices(t *testing.T) {
	db := setupChainDB(t)
	mkCP(t, db, "exito", "Arroz viejo", 3000, 200) // >4 meses → se borra
	mkCP(t, db, "exito", "Arroz nuevo", 2800, 5)
	purged := services.PurgeOldChainPrices(db)
	assert.Equal(t, int64(1), purged)
	var remaining int64
	db.Model(&models.ChainPrice{}).Count(&remaining)
	assert.Equal(t, int64(1), remaining)
}
