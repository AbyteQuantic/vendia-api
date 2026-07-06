// Spec: specs/096-foto-referencia-verificada/spec.md
package database

import (
	"fmt"
	"log"

	"gorm.io/gorm"
)

// BackfillCatalogStatus marks pre-existing Open Food Facts catalog rows
// (source='off', already carrying a real image_url) as status='verified',
// so they're immediately eligible for suggestion without waiting for the
// new discovery job to re-fetch data it already has.
//
// Idempotency (Art. II): only touches rows with status='pending' (the
// default for every row before this backfill and for any not yet reviewed)
// — a row already 'verified' or 'stale' (set by the discovery/refresh job
// or a prior run of this backfill) is never re-touched. Safe to run on
// every boot.
func BackfillCatalogStatus(db *gorm.DB) (int, error) {
	res := db.Exec(`
		UPDATE catalog_products
		SET status = 'verified',
		    verified_at = COALESCE(fetched_at, CURRENT_TIMESTAMP),
		    license = 'CC-BY-SA'
		WHERE source = 'off' AND image_url != '' AND status = 'pending'`)
	if res.Error != nil {
		return 0, fmt.Errorf("backfill catalog status: %w", res.Error)
	}
	touched := int(res.RowsAffected)
	if touched > 0 {
		log.Printf("[BOOTSTRAP] backfill catalog status: %d fotos OFF existentes marcadas verified", touched)
	}
	return touched, nil
}
