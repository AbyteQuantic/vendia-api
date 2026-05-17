// Spec: specs/008-planes-suscripcion-epayco/spec.md
package database

import (
	"fmt"
	"log"
	"time"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// SeedTenantSubscriptions backfills a courtesy 14-day TRIAL for every
// tenant that has no tenant_subscriptions row (Feature 008 — FR-03 /
// AC-02).
//
// Why this exists: registrations before Feature 008 never created the
// subscription row (the DB trigger that was supposed to do it never
// fires under Render's AutoMigrate-only deploy). Those tenants are
// stranded — the PremiumAuth middleware 403s any premium request
// because there is no row to evaluate. This backfill self-heals them
// on boot, the same pattern as BackfillBranchIDs.
//
// Idempotency (Art. II): the function inserts only for tenants whose id
// is absent from tenant_subscriptions. A tenant that already has a row
// — TRIAL, FREE, PRO_ACTIVE, anything — is skipped untouched, so this
// NEVER downgrades a paying customer. Running it on every boot is safe;
// a fully-seeded database makes it a no-op.
//
// Returns the number of subscriptions created. Errors are returned (not
// fatal) — the caller in cmd/server logs and continues, because a
// stranded tenant is preferable to a crashing deploy (Art. X spirit).
func SeedTenantSubscriptions(db *gorm.DB) (int, error) {
	// Find tenant ids with no matching subscription. A LEFT JOIN +
	// IS NULL keeps it to one round-trip even at 10k tenants.
	var orphanIDs []string
	err := db.Table("tenants AS t").
		Select("t.id").
		Joins("LEFT JOIN tenant_subscriptions ts ON ts.tenant_id = t.id").
		Where("ts.tenant_id IS NULL AND t.deleted_at IS NULL").
		Scan(&orphanIDs).Error
	if err != nil {
		return 0, fmt.Errorf("scan tenants without subscription: %w", err)
	}
	if len(orphanIDs) == 0 {
		return 0, nil
	}

	trialEnds := time.Now().Add(models.TrialDays * 24 * time.Hour)
	rows := make([]models.TenantSubscription, 0, len(orphanIDs))
	for _, id := range orphanIDs {
		rows = append(rows, models.TenantSubscription{
			TenantID:    id,
			Status:      models.SubscriptionStatusTrial,
			Plan:        models.SubscriptionPlanFree,
			TrialEndsAt: &trialEnds,
		})
	}

	if err := db.Create(&rows).Error; err != nil {
		return 0, fmt.Errorf("create backfill subscriptions: %w", err)
	}

	log.Printf("[BOOTSTRAP] backfilled %d tenant subscriptions (TRIAL %dd)",
		len(rows), models.TrialDays)
	return len(rows), nil
}
