// Spec: specs/042-modulo-eventos/spec.md
package models

import "time"

// Scan types for check-in (entrada) and check-out (salida). The double scan
// backs the permanence requirement for certificate eligibility (decision #3).
const (
	ScanTypeIn  = "in"
	ScanTypeOut = "out"
)

// EventScan is one check-in/out scan of an attendee's badge QR. It is
// offline-first: the organizer's device may record scans without network and
// sync them later via POST /api/v1/sync/batch. The composite unique index
// (registration_id, session_index, scan_type) makes a repeated scan a no-op,
// preventing double counting even when two devices sync independently
// (spec FR-15, AC-11, decision R-03).
type EventScan struct {
	BaseModel

	TenantID       string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	RegistrationID string `gorm:"type:uuid;not null;index:idx_event_scan_unique,unique,priority:1" json:"registration_id"`
	SessionIndex   int    `gorm:"not null;default:0;index:idx_event_scan_unique,unique,priority:2" json:"session_index"`
	ScanType       string `gorm:"not null;index:idx_event_scan_unique,unique,priority:3" json:"scan_type"`

	ScannedAt time.Time `json:"scanned_at"`
	ScannedBy *string   `gorm:"type:uuid" json:"scanned_by,omitempty"`
}
