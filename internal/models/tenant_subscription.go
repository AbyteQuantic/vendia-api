package models

import "time"

// Subscription lifecycle states — see migration 022.
// The four-state machine is intentionally small: the billing engine
// needs to answer one question ("does this tenant get premium?"), and
// the dashboard needs one badge per tenant. Additional nuance (who
// downgraded, when, why) belongs in an audit log, not in the enum.
const (
	SubscriptionStatusTrial      = "TRIAL"
	SubscriptionStatusFree       = "FREE"
	SubscriptionStatusProActive  = "PRO_ACTIVE"
	SubscriptionStatusProPastDue = "PRO_PAST_DUE"
)

// ValidSubscriptionStatuses mirrors the DB CHECK constraint in
// migration 022. Keep in sync — the migration enforces it at write
// time, and the middleware / admin handler rely on it at read time.
var ValidSubscriptionStatuses = map[string]struct{}{
	SubscriptionStatusTrial:      {},
	SubscriptionStatusFree:       {},
	SubscriptionStatusProActive:  {},
	SubscriptionStatusProPastDue: {},
}

// TenantSubscription is the 1:1 row the DB trigger creates for every
// new tenant. The struct lives alongside the tenant but is kept as a
// separate table (not a column) so future billing primitives —
// payment history, dunning state, plan tiers — attach here without
// bloating the hot-path `tenants` table the POS hits on every sale.
type TenantSubscription struct {
	TenantID    string     `gorm:"type:uuid;primaryKey" json:"tenant_id"`
	Status      string     `gorm:"type:varchar(32);not null;default:'TRIAL'" json:"status"`
	TrialEndsAt *time.Time `json:"trial_ends_at"`
	// Timestamps are managed by GORM — the migration sets DB defaults of
	// NOW() so inserts via raw SQL (e.g. the trigger) also get populated;
	// the struct tags stay minimal so sqlite-backed tests can AutoMigrate.
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

// TableName keeps GORM aligned with the migration (plural + underscores).
func (TenantSubscription) TableName() string { return "tenant_subscriptions" }

// IsPremium answers the authorisation question: should this tenant see
// PRO-gated modules? TRIAL is premium while trial_ends_at is in the
// future; PRO_ACTIVE is always premium. Everything else falls through
// to the soft paywall.
func (s *TenantSubscription) IsPremium(now time.Time) bool {
	if s == nil {
		return false
	}
	switch s.Status {
	case SubscriptionStatusProActive:
		return true
	case SubscriptionStatusTrial:
		return s.TrialEndsAt != nil && s.TrialEndsAt.After(now)
	default:
		return false
	}
}

// TrialDaysRemaining rounds UP so a tenant with 0.1 days left still
// sees "1 día" in the dashboard. Returns 0 for non-trial subscriptions
// and for expired trials (the caller decides whether to show "expired"
// separately via Status).
func (s *TenantSubscription) TrialDaysRemaining(now time.Time) int {
	if s == nil || s.Status != SubscriptionStatusTrial || s.TrialEndsAt == nil {
		return 0
	}
	remaining := s.TrialEndsAt.Sub(now)
	if remaining <= 0 {
		return 0
	}
	days := int(remaining.Hours() / 24)
	if remaining > time.Duration(days)*24*time.Hour {
		days++
	}
	return days
}
