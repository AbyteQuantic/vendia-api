// Spec: specs/036-dashboard-adaptativo-onboarding/spec.md
package database

import (
	"errors"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"

	"vendia-backend/internal/models"
)

// onboardingBackfillMarker is the BootstrapMarker key that records the
// F036 onboarding backfill has already run.
const onboardingBackfillMarker = "f036_onboarding_completed_backfill"

// BackfillOnboardingCompleted marks every tenant that existed BEFORE the
// F036 deploy as onboarding_completed=true, so an established business
// never gets the first-run onboarding wizard (Spec F036 D4 / AC-08).
//
// Why a one-shot guard instead of the usual run-every-boot backfill:
// after this deploy, a brand-new registration legitimately lands with
// onboarding_completed=false (it MUST see the wizard). A blind
// `UPDATE ... WHERE onboarding_completed = false` on a later boot would
// wrongly flip those pending new tenants. So the function guards itself
// on a BootstrapMarker row — it does the UPDATE exactly once, the very
// first boot after the F036 deploy, then short-circuits forever after.
//
// Idempotency (Art. II): the marker check makes a second call a no-op
// that returns 0. The marker insert and the bulk UPDATE run in the same
// transaction so a crash mid-backfill never leaves the marker without
// the UPDATE (or vice versa).
//
// Migrations (Art. X): Render deploys run GORM AutoMigrate only, never
// the goose .sql files, so this backfill lives in the Go bootstrap.
//
// Returns the number of tenants flipped. Errors are wrapped with %w; the
// caller logs them and continues — a tenant stranded with the wizard is
// preferable to a crashing deploy.
func BackfillOnboardingCompleted(db *gorm.DB) (int, error) {
	// Guard: if the marker exists, the backfill already ran — no-op.
	var marker models.BootstrapMarker
	err := db.Where("name = ?", onboardingBackfillMarker).First(&marker).Error
	if err == nil {
		return 0, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("check onboarding backfill marker: %w", err)
	}

	touched := 0
	txErr := db.Transaction(func(tx *gorm.DB) error {
		// Flip every tenant currently pending. On the FIRST boot after
		// the deploy this is exactly the set of pre-F036 tenants, since
		// no post-deploy registration has had time to land.
		res := tx.Exec(
			`UPDATE tenants SET onboarding_completed = ? WHERE onboarding_completed = ?`,
			true, false)
		if res.Error != nil {
			return fmt.Errorf("backfill onboarding_completed: %w", res.Error)
		}
		touched = int(res.RowsAffected)

		// Record the one-shot guard inside the same transaction.
		if err := tx.Create(&models.BootstrapMarker{
			Name:  onboardingBackfillMarker,
			RanAt: time.Now(),
		}).Error; err != nil {
			return fmt.Errorf("record onboarding backfill marker: %w", err)
		}
		return nil
	})
	if txErr != nil {
		return 0, txErr
	}

	if touched > 0 {
		log.Printf("[BOOTSTRAP] backfill onboarding_completed: %d tenants pre-F036 marcados", touched)
	}
	return touched, nil
}
