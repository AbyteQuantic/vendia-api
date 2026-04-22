package models

import "time"

// Ticket lifecycle — deliberately small until real usage tells us a
// middle state (IN_PROGRESS, WAITING_ON_USER) pays for itself.
const (
	TicketStatusOpen     = "OPEN"
	TicketStatusResolved = "RESOLVED"
)

var ValidTicketStatuses = map[string]struct{}{
	TicketStatusOpen:     {},
	TicketStatusResolved: {},
}

// SupportTicket is the row inserted when a tenant submits the support
// form. Keeping `user_id` nullable lets us preserve the ticket history
// even if the user who raised it later gets hard-deleted (the DB rule
// is ON DELETE SET NULL — see migration 023).
type SupportTicket struct {
	ID        string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID  string    `gorm:"type:uuid;index;not null" json:"tenant_id"`
	UserID    *string   `gorm:"type:uuid" json:"user_id,omitempty"`
	Subject   string    `gorm:"size:160;not null" json:"subject"`
	Message   string    `gorm:"type:text;not null" json:"message"`
	Status    string    `gorm:"type:varchar(16);not null;default:'OPEN'" json:"status"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

func (SupportTicket) TableName() string { return "support_tickets" }
