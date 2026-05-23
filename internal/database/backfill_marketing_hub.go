// Spec: specs/037-reel-capacidades-dashboard/spec.md
package database

import (
	"errors"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"

	"vendia-backend/internal/models"
)

// f037Backfill bundles the metadata of each one-shot F037 capability
// backfill: a stable BootstrapMarker name, the target column on the
// tenants table, and the source table whose presence of at least one
// row for the tenant signals the capability is in use.
//
// F037 reclassifies several modules from "byType / always available" to
// "opt-in via capability flag". Without the backfill, every tenant who
// was already using one of those modules (e.g. a tendero with combo-
// promos already created) would see the module disappear from their
// Dashboard the moment F037 deploys. The backfill flips the matching
// enable_* flag to true for any tenant carrying data in the source
// table, mirroring the F036 onboarding backfill pattern.
type f037Backfill struct {
	// marker is the BootstrapMarker key that records this backfill ran.
	marker string
	// column is the tenants column to flip to true.
	column string
	// table is the source table to look for at least one row per tenant.
	// Rows must have a tenant_id column — every multi-tenant table in
	// VendIA does.
	table string
}

// f037Backfills enumerates every reclassification backfill F037 needs.
// Each entry runs exactly once thanks to its BootstrapMarker guard;
// subsequent boots short-circuit. Idempotent and safe to re-run.
var f037Backfills = []f037Backfill{
	// Marketing Hub bundle: combo-promos + banners + public catalog.
	// Source of truth = at least one Promotion row (combos live there;
	// banners populate BannerImageURL on the same table). A merchant
	// who built any campaign before the F037 deploy keeps the module.
	{
		marker: "f037_marketing_hub_backfill",
		column: "enable_marketing_hub",
		table:  "promotions",
	},
	// Recetas: any Recipe row — drove the kitchen mode for restaurantes /
	// comidas_rapidas under F036's byType layer.
	{
		marker: "f037_recipes_backfill",
		column: "enable_recipes",
		table:  "recipes",
	},
	// Insumos: any Ingredient row — Feature 001 raw-material inventory.
	{
		marker: "f037_supplies_backfill",
		column: "enable_supplies",
		table:  "ingredients",
	},
	// Trabajos de muebles: any WorkOrder row — Feature 003.
	{
		marker: "f037_furniture_jobs_backfill",
		column: "enable_furniture_jobs",
		table:  "work_orders",
	},
	// Órdenes de compra: any PurchaseOrder row — Feature 002.
	{
		marker: "f037_purchase_orders_backfill",
		column: "enable_purchase_orders",
		table:  "purchase_orders",
	},
}

// BackfillF037Capabilities runs every F037 one-shot reclassification
// backfill in sequence. Each child backfill is guarded by its own
// BootstrapMarker, so a re-run is a no-op. Errors from one entry are
// logged and DO NOT abort the rest — a tenant stranded without one of
// the optional capabilities is preferable to a crashing deploy. Returns
// the total number of tenants touched across every backfill so the
// boot log can summarise the work in a single line.
func BackfillF037Capabilities(db *gorm.DB) (int, error) {
	total := 0
	for _, bf := range f037Backfills {
		touched, err := runF037Backfill(db, bf)
		if err != nil {
			// Log and continue — see rationale above. The marker is
			// only written on success so a transient failure will
			// retry on the next boot.
			log.Printf("[BOOTSTRAP] F037 backfill %s failed: %v", bf.marker, err)
			continue
		}
		total += touched
		if touched > 0 {
			log.Printf("[BOOTSTRAP] F037 backfill %s flipped %d tenants",
				bf.marker, touched)
		}
	}
	return total, nil
}

// runF037Backfill executes a single F037 capability backfill. Flow:
//
//  1. If the marker row exists → no-op, return 0.
//  2. Otherwise, in one transaction: UPDATE every tenant that has at
//     least one row in the source table to flip the target column to
//     true, then insert the marker row.
//
// Atomicity (Art. II): marker insert and UPDATE share the transaction,
// so a crash mid-backfill never leaves the marker without the UPDATE
// or vice versa.
func runF037Backfill(db *gorm.DB, bf f037Backfill) (int, error) {
	var marker models.BootstrapMarker
	err := db.Where("name = ?", bf.marker).First(&marker).Error
	if err == nil {
		return 0, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("check marker %s: %w", bf.marker, err)
	}

	// Skip silently if the source table doesn't exist yet. AutoMigrate
	// has already run by the time we get here so every Go-modelled
	// table is present, but tests sometimes hand us a partial schema.
	if !db.Migrator().HasTable(bf.table) {
		// Still record the marker so we don't keep checking on every
		// boot. The table is permanently absent for this deploy → the
		// backfill is a no-op forever.
		if err := db.Create(&models.BootstrapMarker{
			Name:  bf.marker,
			RanAt: time.Now(),
		}).Error; err != nil {
			return 0, fmt.Errorf("record marker %s (no source table): %w", bf.marker, err)
		}
		return 0, nil
	}

	touched := 0
	txErr := db.Transaction(func(tx *gorm.DB) error {
		// Build the UPDATE: flip the column to true for any tenant
		// who already has at least one row in the source table AND
		// hasn't been flipped yet. Parameter binding is unnecessary
		// for the column/table names (they come from a hard-coded
		// allow list above, never from user input) — but the WHERE
		// values stay parameterised. We use `EXISTS` rather than `IN`
		// so a tenant with thousands of rows in the source table
		// still produces a single index lookup per tenant.
		query := fmt.Sprintf(`
			UPDATE tenants
			SET %s = ?
			WHERE %s = ?
			  AND EXISTS (
			    SELECT 1 FROM %s
			    WHERE %s.tenant_id = tenants.id
			  )
		`, bf.column, bf.column, bf.table, bf.table)
		res := tx.Exec(query, true, false)
		if res.Error != nil {
			return fmt.Errorf("update %s: %w", bf.column, res.Error)
		}
		touched = int(res.RowsAffected)

		if err := tx.Create(&models.BootstrapMarker{
			Name:  bf.marker,
			RanAt: time.Now(),
		}).Error; err != nil {
			return fmt.Errorf("record marker %s: %w", bf.marker, err)
		}
		return nil
	})
	if txErr != nil {
		return 0, txErr
	}
	return touched, nil
}
