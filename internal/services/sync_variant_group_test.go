// Spec: specs/095-variantes-producto/spec.md (AC-07)
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

func setupSyncVariantGroupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Tenant{}, &models.ProductVariantGroup{}))
	return db
}

// Bug real de esta sesión: un `entity` sin `case` explícito en
// processOperation cae al `default: return true, nil` — la op se marca
// como aplicada, el cliente la borra de su cola, pero el servidor NUNCA la
// persistió (ya pasó 2 veces con otras entidades). Este test confirma que
// "product_variant_group" tiene un case real, no el default silencioso.
func TestSyncBatch_ProductVariantGroup_Persists(t *testing.T) {
	db := setupSyncVariantGroupDB(t)
	tenantID := "11111111-1111-4111-8111-111111111111"
	groupID := "a0000000-0000-4000-8000-000000000095"

	require.NoError(t, db.Create(&models.Tenant{BaseModel: models.BaseModel{ID: tenantID}}).Error)

	svc := services.NewSyncService(db)
	now := time.Now()
	op := services.SyncOperation{
		Entity:          "product_variant_group",
		Action:          "create",
		ID:              groupID,
		ClientUpdatedAt: now,
		Data: map[string]any{
			"tenant_id":        tenantID,
			"name":             "Camiseta Básica",
			"category":         "Ropa",
			"attribute_labels": `["Talla","Color"]`,
			"created_at":       now,
			"updated_at":       now,
		},
	}
	req := services.SyncRequest{Operations: []services.SyncOperation{op}}

	resp, err := svc.ProcessBatch(tenantID, req)
	require.NoError(t, err)

	var group models.ProductVariantGroup
	err = db.First(&group, "id = ?", groupID).Error
	require.NoError(t, err, "el grupo DEBE existir en la base — si esto falla, la op cayó al default silencioso")
	assert.Equal(t, "Camiseta Básica", group.Name)
	assert.Empty(t, resp.Conflicts, "una creación limpia no debe reportar conflicto")
}
