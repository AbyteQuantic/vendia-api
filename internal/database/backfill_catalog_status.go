// Spec: specs/096-foto-referencia-verificada/spec.md (Adenda A)
package database

import (
	"fmt"
	"log"

	"gorm.io/gorm"
)

// RevertOffAutoVerifiedCatalogRows corrects a mistake from the original
// (pre-Adenda A) version of this backfill: it used to mark every
// pre-existing Open Food Facts row as status='verified' on boot. Adenda A
// (2026-07-06): el fundador probó OFF en producción y encontró cobertura
// pobre de Colombia + fotos de mala calidad — OFF NUNCA debe ser la fuente
// de la verdad. Solo 2+ tenants distintos compartiendo su propia foto
// (CatalogService.ShareProductPhotoToCatalog) puede verificar una fila.
//
// This reverts any row the old backfill already verified back to
// 'pending' — those rows had zero tenant confirmation, only an OFF import.
//
// Idempotency (Art. II): only touches rows with source='off' AND
// status='verified' — once reverted, a row is 'pending' and never matches
// again. Safe to run on every boot.
func RevertOffAutoVerifiedCatalogRows(db *gorm.DB) (int, error) {
	res := db.Exec(`
		UPDATE catalog_products
		SET status = 'pending', verified_at = NULL
		WHERE source = 'off' AND status = 'verified'`)
	if res.Error != nil {
		return 0, fmt.Errorf("revert off auto-verified catalog rows: %w", res.Error)
	}
	touched := int(res.RowsAffected)
	if touched > 0 {
		log.Printf("[BOOTSTRAP] revert off auto-verified catalog rows: %d filas OFF sin consenso de tenants devueltas a pending", touched)
	}
	return touched, nil
}
