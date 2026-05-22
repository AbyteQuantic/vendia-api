// Spec: specs/036-dashboard-adaptativo-onboarding/spec.md
package models

import "time"

// BootstrapMarker records that a one-shot bootstrap step has already run.
// Some backfills are NOT naturally idempotent — F036's onboarding
// backfill, for instance, marks every pre-existing tenant as
// "onboarding_completed", but after the deploy a brand-new tenant
// legitimately has onboarding_completed=false. Re-running the blind
// UPDATE on a later boot would wrongly flip that new tenant.
//
// A backfill that needs to run exactly once inserts a row here keyed by
// a stable name and guards itself on the row's presence. The table is
// append-only — rows are never deleted, so a step that has run stays
// recorded forever.
type BootstrapMarker struct {
	// Name is the unique key of the bootstrap step (e.g.
	// "f036_onboarding_completed_backfill").
	Name string `gorm:"primaryKey;type:varchar(128)" json:"name"`
	// RanAt is the timestamp the step completed.
	RanAt time.Time `gorm:"not null" json:"ran_at"`
}

// TableName pins the table name so it reads clearly in the schema.
func (BootstrapMarker) TableName() string { return "bootstrap_markers" }
