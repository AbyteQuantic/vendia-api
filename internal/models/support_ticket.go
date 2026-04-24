package models

import "time"

const (
	TicketStatusOpen       = "OPEN"
	TicketStatusInProgress = "IN_PROGRESS"
	TicketStatusResolved   = "RESOLVED"

	TicketPriorityLow    = "LOW"
	TicketPriorityNormal = "NORMAL"
	TicketPriorityHigh   = "HIGH"
	TicketPriorityUrgent = "URGENT"

	TicketCategoryBilling = "BILLING"
	TicketCategoryBug     = "BUG"
	TicketCategoryFeature = "FEATURE"
	TicketCategoryOther   = "OTHER"
)

var ValidTicketStatuses = map[string]struct{}{
	TicketStatusOpen:       {},
	TicketStatusInProgress: {},
	TicketStatusResolved:   {},
}

var ValidTicketPriorities = map[string]struct{}{
	TicketPriorityLow:    {},
	TicketPriorityNormal: {},
	TicketPriorityHigh:   {},
	TicketPriorityUrgent: {},
}

var ValidTicketCategories = map[string]struct{}{
	TicketCategoryBilling: {},
	TicketCategoryBug:     {},
	TicketCategoryFeature: {},
	TicketCategoryOther:   {},
}

type SupportTicket struct {
	ID        string    `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID  string    `gorm:"type:uuid;index;not null" json:"tenant_id"`
	UserID    *string   `gorm:"type:uuid" json:"user_id,omitempty"`
	Subject   string    `gorm:"size:160;not null" json:"subject"`
	Status    string    `gorm:"type:varchar(16);not null;default:'OPEN'" json:"status"`
	Priority  string    `gorm:"type:varchar(16);not null;default:'NORMAL'" json:"priority"`
	Category  string    `gorm:"type:varchar(16);not null;default:'OTHER'" json:"category"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	Messages []SupportTicketMessage `gorm:"foreignKey:TicketID" json:"messages,omitempty"`

	// Virtual fields for Admin UI
	BusinessName string `gorm:"-" json:"business_name,omitempty"`
}

func (SupportTicket) TableName() string { return "support_tickets" }

type SupportTicketMessage struct {
	ID         string    `gorm:"type:uuid;primaryKey" json:"id"`
	TicketID   string    `gorm:"type:uuid;index;not null" json:"ticket_id"`
	SenderType string    `gorm:"type:varchar(16);not null" json:"sender_type"` // TENANT, ADMIN
	SenderID   string    `gorm:"type:uuid;not null" json:"sender_id"`
	Content    string    `gorm:"type:text;not null" json:"content"`
	CreatedAt  time.Time `gorm:"not null" json:"created_at"`
}

func (SupportTicketMessage) TableName() string { return "support_ticket_messages" }
