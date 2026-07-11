// Spec: specs/104-moderacion-f1-lexico/spec.md
package database

import (
	"gorm.io/gorm"

	"vendia-backend/internal/models"
	"vendia-backend/internal/moderation"
)

// BackfillProductModeration evalúa el léxico sobre las filas creadas ANTES
// del feature (moderation_status vacío). CPU puro, por lotes, idempotente:
// tras la primera pasada toda fila tiene status y el WHERE no encuentra nada.
func BackfillProductModeration(db *gorm.DB) (int, error) {
	touched := 0
	for {
		var batch []models.Product
		if err := db.Select("id", "tenant_id", "name", "category", "description").
			Where("moderation_status IS NULL OR moderation_status = ''").
			Limit(500).Find(&batch).Error; err != nil {
			return touched, err
		}
		if len(batch) == 0 {
			return touched, nil
		}
		for _, p := range batch {
			v := moderation.EvaluateProduct(p.Name, p.Category, p.Description)
			if err := db.Model(&models.Product{}).Where("id = ?", p.ID).
				UpdateColumns(map[string]any{
					"moderation_status":   v.Status,
					"moderation_category": v.Category,
				}).Error; err != nil {
				return touched, err
			}
			if v.Status != moderation.StatusAllowed {
				logRow := models.ModerationLog{
					TenantID: p.TenantID, EntityType: "product", EntityID: p.ID,
					EntityName: p.Name, Verdict: v.Status, Category: v.Category,
					Actor: "lexicon:f1-backfill",
				}
				_ = db.Create(&logRow).Error
			}
			touched++
		}
	}
}
