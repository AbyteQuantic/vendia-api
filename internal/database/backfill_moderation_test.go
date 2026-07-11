// Spec: specs/104-moderacion-f1-lexico/spec.md
package database

import (
	"testing"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// AC-07: el backfill evalúa filas pre-feature (status vacío) y es idempotente.
func TestBackfillProductModeration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}, &models.ModerationLog{}))

	// Filas "legacy": insertadas por SQL crudo sin status (hook no corre).
	require.NoError(t, db.Exec(`
		INSERT INTO products (id, tenant_id, name, price, moderation_status, created_at)
		VALUES ('p1', 't1', 'Volador pólvora x6', 4000, '', datetime('now')),
		       ('p2', 't1', 'Arroz Diana 500g', 3000, '', datetime('now'))
	`).Error)

	touched, err := BackfillProductModeration(db)
	require.NoError(t, err)
	assert.Equal(t, 2, touched)

	var p1, p2 models.Product
	db.First(&p1, "id = 'p1'")
	db.First(&p2, "id = 'p2'")
	assert.Equal(t, "blocked", p1.ModerationStatus)
	assert.Equal(t, "polvora", p1.ModerationCategory)
	assert.Equal(t, "allowed", p2.ModerationStatus)

	// Idempotente: segunda pasada no toca nada.
	touched2, err := BackfillProductModeration(db)
	require.NoError(t, err)
	assert.Equal(t, 0, touched2)
}
