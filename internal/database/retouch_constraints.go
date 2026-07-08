// Spec: specs/101-retocar-fotos-inventario/spec.md
package database

import "gorm.io/gorm"

// ApplyRetouchIndexes — Spec 101 / D4. Índice UNIQUE parcial que hace físico
// el invariante "un producto no vive en dos lotes de retoque ACTIVOS a la
// vez" (FR-13): es la barrera total contra re-encolar el mismo producto
// mientras su ítem sigue queued/processing/ready_for_review. Los handlers
// mapean su violación a skipped[] (idempotencia por ítem). Parcial: los
// estados terminales (confirmed/discarded/failed/skipped_stale/canceled) no
// bloquean re-encolar cuando la foto vuelve a estar cruda.
//
// El SQL es portable (Postgres y SQLite soportan índices parciales con
// IF NOT EXISTS), así que los tests lo aplican tal cual sobre la SQLite
// in-memory. En producción lo llama el bootstrap tolerante (patrón Spec 100:
// log + seguir arrancando, jamás tumbar el arranque — Art. X).
func ApplyRetouchIndexes(db *gorm.DB) error {
	return db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_retouch_items_active_product
		 ON retouch_items (tenant_id, product_id)
		 WHERE status IN ('queued','processing','ready_for_review')
		   AND deleted_at IS NULL`,
	).Error
}
