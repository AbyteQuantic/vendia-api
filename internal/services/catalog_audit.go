// Spec: specs/041-catalogo-dinamico-modulos-tipos/spec.md
//
// Auditoría del catálogo (D9): registra el antes/después de cada escritura
// (módulos, tipos, relaciones, overrides) con autor y fecha. Un cambio del
// catálogo afecta a TODAS las tiendas, así que el log es obligatorio.

package services

import (
	"encoding/json"
	"log"

	"gorm.io/gorm"

	"vendia-backend/internal/models"
)

// Acciones de auditoría.
const (
	AuditCreate  = "create"
	AuditUpdate  = "update"
	AuditArchive = "archive"
	AuditDelete  = "delete"
	AuditRelate  = "relate"
)

// jsonString serializa v a JSON; "" para nil. Nunca rompe la operación.
func jsonString(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// LogCatalogChange escribe una entrada de auditoría. Falla silenciosa
// (registra en el log del servidor) — la auditoría nunca debe tumbar la
// operación de negocio que la disparó.
func LogCatalogChange(db *gorm.DB, actorID, actorName, entityType, entityID, action string, before, after any) {
	entry := models.CatalogAuditLog{
		ActorID:    actorID,
		ActorName:  actorName,
		EntityType: entityType,
		EntityID:   entityID,
		Action:     action,
		Before:     jsonString(before),
		After:      jsonString(after),
	}
	if err := db.Create(&entry).Error; err != nil {
		log.Printf("[catalog-audit] no se pudo registrar %s %s/%s: %v",
			action, entityType, entityID, err)
	}
}
