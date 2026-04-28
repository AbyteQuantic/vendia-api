package models

import "time"

// CartSession marks a (tenant, branch, cart_index) slot as held by a
// specific user. The POS uses 10 cart "tabs" (C1..C10); this table
// surfaces who is currently using each slot so a second device can't
// edit the same cart simultaneously and stomp the cashier's work.
//
// Lifecycle:
//   - claim:    upsert a row when the user opens the tab
//   - heartbeat: refresh LastHeartbeat every ~30s while the user
//     stays on that tab
//   - release:  delete the row on tab switch / app close
//   - stale auto-release: handlers prune rows whose LastHeartbeat is
//     older than `staleAfter` (default 5 minutes) before returning
//     the live snapshot, so a crashed device doesn't lock a slot
//     indefinitely.
//
// The unique index (tenant_id, branch_id, cart_index) is the cardinal
// invariant: only one active holder per slot. NULL branch_id is
// treated by Postgres's UNIQUE-with-NULL semantics — see the explicit
// partial-index workaround in the migration / AutoMigrate hook.
type CartSession struct {
	BaseModel

	TenantID      string    `gorm:"type:uuid;not null;index" json:"tenant_id"`
	BranchID      *string   `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	CartIndex     int       `gorm:"not null" json:"cart_index"`
	UserID        string    `gorm:"type:uuid;not null;index" json:"user_id"`
	EmployeeName  string    `gorm:"not null;default:''" json:"employee_name"`
	Role          string    `gorm:"not null;default:''" json:"role"`
	// Defaults populated in the handler so the GORM tag stays
	// dialect-agnostic (Postgres prod / SQLite tests). Setting
	// `default:NOW()` only worked on Postgres.
	StartedAt     time.Time `gorm:"not null" json:"started_at"`
	LastHeartbeat time.Time `gorm:"not null" json:"last_heartbeat"`
}

// TableName keeps GORM's pluralisation predictable across Postgres /
// SQLite (test) so the partial-index name in database.go stays valid.
func (CartSession) TableName() string { return "cart_sessions" }
