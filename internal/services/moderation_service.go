// Spec: specs/104-moderacion-f1-lexico/spec.md
package services

import (
	"log"

	"gorm.io/gorm"

	"vendia-backend/internal/models"
	"vendia-backend/internal/moderation"
)

// EnsureProductModeration re-evalúa el léxico sobre la fila REAL de un
// producto y persiste el veredicto si cambió. Cubre los caminos de escritura
// por MAPA, donde el hook BeforeSave del modelo no corre (UpdateProduct con
// Updates(map) y el sync offline que crea/actualiza desde op.Data).
// Fail-silent a propósito: la moderación jamás rompe la escritura del
// tendero; un fallo aquí solo deja el status anterior (o vacío, que el
// backfill de bootstrap re-evalúa).
func EnsureProductModeration(db *gorm.DB, tenantID, productID string) {
	if productID == "" {
		return
	}
	var p models.Product
	if err := db.Select("id", "tenant_id", "name", "category", "description",
		"moderation_status", "moderation_category").
		Where("id = ? AND tenant_id = ?", productID, tenantID).
		First(&p).Error; err != nil {
		return
	}
	v := moderation.EvaluateProduct(p.Name, p.Category, p.Description)
	if v.Status == p.ModerationStatus && v.Category == p.ModerationCategory {
		return
	}
	// UpdateColumns: sin hooks (evita re-evaluar en BeforeSave) y sin tocar
	// updated_at (no es un cambio del tendero, es metadata de moderación).
	if err := db.Model(&models.Product{}).Where("id = ?", p.ID).
		UpdateColumns(map[string]any{
			"moderation_status":   v.Status,
			"moderation_category": v.Category,
		}).Error; err != nil {
		log.Printf("[MODERATION] no se pudo persistir veredicto de %s: %v", p.ID, err)
		return
	}
	if v.Status != moderation.StatusAllowed {
		logRow := models.ModerationLog{
			TenantID:   p.TenantID,
			EntityType: "product",
			EntityID:   p.ID,
			EntityName: p.Name,
			Verdict:    v.Status,
			Category:   v.Category,
			Actor:      "lexicon:f1",
		}
		if err := db.Create(&logRow).Error; err != nil {
			log.Printf("[MODERATION] no se pudo escribir moderation_log (%s): %v", p.ID, err)
		}
	}
}
