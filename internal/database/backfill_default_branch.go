// Spec: specs/014-inventario-solido-scope-sede/spec.md
package database

import (
	"fmt"
	"log"

	"gorm.io/gorm"
)

// BackfillDefaultBranch marks the tenant's oldest branch (same
// resolution as BackfillBranchIDs — defaultBranchByTenant) as
// is_default=true, for every tenant that doesn't already have one.
//
// Why: is_default was added after several tenants already had 2+
// branches. Without a real default, the frontend's
// BranchProvider.setBranches() falls back to "whichever branch the API
// returns first" — which can be an empty/secondary sede depending on
// branch creation order, silently hiding the tenant's actual inventory
// from the POS "nueva venta" screen (incident 2026-07-05: a tenant's
// Coca-Cola/gaseosas disappeared because the wrong sede auto-selected).
//
// Idempotency (Art. II): a tenant that already has an is_default=true
// branch is skipped entirely, so a second run is a no-op and this never
// overrides a choice made later (e.g. an owner explicitly re-flagging
// their default sede). New tenants set is_default=true explicitly on
// their "Principal" branch at registration, so this only ever touches
// pre-existing tenants — self-limiting, safe to run on every boot.
func BackfillDefaultBranch(db *gorm.DB) (int, error) {
	defaults, err := defaultBranchByTenant(db)
	if err != nil {
		return 0, fmt.Errorf("resolve default branches: %w", err)
	}
	if len(defaults) == 0 {
		return 0, nil
	}

	total := 0
	for tenantID, branchID := range defaults {
		var count int64
		if err := db.Table("branches").
			Where("tenant_id = ? AND is_default = ? AND deleted_at IS NULL", tenantID, true).
			Count(&count).Error; err != nil {
			return total, fmt.Errorf("check existing default for tenant %s: %w", tenantID, err)
		}
		if count > 0 {
			continue
		}
		res := db.Exec("UPDATE branches SET is_default = ? WHERE id = ?", true, branchID)
		if res.Error != nil {
			return total, fmt.Errorf("set default branch for tenant %s: %w", tenantID, res.Error)
		}
		total += int(res.RowsAffected)
	}

	if total > 0 {
		log.Printf("[BOOTSTRAP] backfill is_default: %d sedes marcadas por defecto", total)
	}
	return total, nil
}
