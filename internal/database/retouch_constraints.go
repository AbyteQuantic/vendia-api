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
	stmts := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_retouch_items_active_product
		 ON retouch_items (tenant_id, product_id)
		 WHERE status IN ('queued','processing','ready_for_review')
		   AND deleted_at IS NULL`,
		// Máx 1 lote ACTIVO por tenant (D3) hecho físico: sin esto, dos POST
		// simultáneos (doble-tap, dueño+empleado) crean dos lotes running y
		// el summary solo muestra el más reciente — el progreso del otro
		// queda invisible (AC-09). El handler captura la violación y
		// re-selecciona el lote ganador.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_retouch_batches_active_tenant
		 ON retouch_batches (tenant_id)
		 WHERE status IN ('running','paused_error')
		   AND deleted_at IS NULL`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			return err
		}
	}
	return nil
}
