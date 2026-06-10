// Spec: specs/042-modulo-eventos/spec.md
package models

import (
	"errors"
	"time"
)

// Event types, modalities and statuses. Stored as plain strings so the
// schema stays additive (Art. X) — new values need no migration.
const (
	EventTypeCurso       = "curso"
	EventTypeConferencia = "conferencia"
	EventTypeHackaton    = "hackaton"
	EventTypeOtro        = "otro"

	EventModalityPresencial = "presencial"
	EventModalityVirtual    = "virtual"
	EventModalityHibrido    = "hibrido"

	EventStatusBorrador  = "borrador"
	EventStatusPublicado = "publicado"
	EventStatusArchivado = "archivado"
	EventStatusCancelado = "cancelado"

	// Currencies a paid event can charge in.
	CurrencyCOP = "COP"
	CurrencyUSD = "USD"

	// AttendanceRule decides certificate eligibility (spec §7).
	AttendanceRuleInOut       = "in_out"       // requires entrada + salida
	AttendanceRulePctSessions = "pct_sessions" // requires % of sessions
)

// Event is an organizer's event (course/conference/hackathon/other). It is
// owned by a single tenant and, once published, is promoted in that tenant's
// public catalog. Money never flows through VendIA — the organizer connects
// their own payment rails; VendIA only bridges (spec §1, decision R-01).
type Event struct {
	BaseModel

	TenantID string  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	BranchID *string `gorm:"type:uuid;index" json:"branch_id,omitempty"`

	Type        string `gorm:"not null;default:'otro'" json:"type"`
	Title       string `gorm:"not null" json:"title"`
	Description string `json:"description,omitempty"`

	StartAt  *time.Time `json:"start_at,omitempty"`
	EndAt    *time.Time `json:"end_at,omitempty"`
	Modality string     `gorm:"not null;default:'presencial'" json:"modality"`
	// LocationOrLink is the physical address (presencial) or the join URL
	// (virtual). Híbrido may carry both joined by the UI.
	LocationOrLink string `json:"location_or_link,omitempty"`
	// City and LocationNotes detail a physical venue (ciudad, edificio /
	// indicaciones de cómo llegar). Empty for virtual events.
	City          string `json:"city,omitempty"`
	LocationNotes string `json:"location_notes,omitempty"`

	Capacity int   `gorm:"not null;default:0" json:"capacity"`
	Price    int64 `gorm:"not null;default:0" json:"price"` // entero en la moneda; 0 = gratis

	// Currency of Price: "COP" (default, múltiplo de $50, Art. VII) or "USD".
	Currency string `gorm:"not null;default:'COP'" json:"currency"`

	Status string `gorm:"not null;default:'borrador';index" json:"status"`

	// EnabledPaymentMethods holds which of the organizer's own rails are
	// accepted for this event (e.g. ["epayco","fiado","manual","cobro_digital"]).
	EnabledPaymentMethods []string `gorm:"serializer:json;type:jsonb;default:'[]'" json:"enabled_payment_methods"`

	// PaymentDetails carries, per enabled method, the organizer's payment data
	// (account/Nequi number, instructions) and an optional QR image, so the
	// attendee knows WHERE to pay before reporting the proof (FR-09).
	PaymentDetails []EventPaymentDetail `gorm:"serializer:json;type:jsonb;default:'[]'" json:"payment_details"`

	// Installments — manual mode only in the MVP (spec decision #9). The
	// automatic recurring charge is out of MVP scope.
	InstallmentsEnabled bool `gorm:"default:false" json:"installments_enabled"`
	InstallmentsCount   int  `gorm:"default:0" json:"installments_count"`

	// CustomFields is the organizer-defined inscription form schema.
	CustomFields []EventCustomField `gorm:"serializer:json;type:jsonb;default:'[]'" json:"custom_fields"`

	// Sessions backs multi-session attendance. The MVP uses a single
	// implicit session (entrada/salida) when empty.
	Sessions []EventSession `gorm:"serializer:json;type:jsonb;default:'[]'" json:"sessions"`

	AttendanceRule string `gorm:"not null;default:'in_out'" json:"attendance_rule"`
	AttendancePct  int    `gorm:"default:100" json:"attendance_pct"`

	// Badge/Certificate templates are design blobs (config + image_url),
	// produced by the organizer with optional Gemini assistance.
	BadgeTemplate       EventTemplate `gorm:"serializer:json;type:jsonb;default:'{}'" json:"badge_template"`
	CertificateTemplate EventTemplate `gorm:"serializer:json;type:jsonb;default:'{}'" json:"certificate_template"`

	// PosterTemplate is the marketing piece (afiche publicitario) shown in
	// the public catalog alongside products — the image the WhatsApp link
	// surfaces. Unlike the badge/certificate it carries NO QR; it sells the
	// event. Produced by the organizer with optional Gemini assistance.
	PosterTemplate EventTemplate `gorm:"serializer:json;type:jsonb;default:'{}'" json:"poster_template"`
}

// EventPaymentDetail are the organizer's payment data for ONE method: free
// text instructions (account/Nequi number, name, etc.) and an optional QR
// image. Shown to the attendee on the carné when the payment is pending.
type EventPaymentDetail struct {
	Method       string `json:"method"`        // matches an EnabledPaymentMethods value
	Instructions string `json:"instructions"`  // número de cuenta / Nequi / indicaciones
	QRImageURL   string `json:"qr_image_url"`  // imagen QR opcional (R2 o data URL)
}

// EventCustomField is one organizer-defined inscription field.
type EventCustomField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text|number|select|bool
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"`
}

// EventSession is one session of a multi-session event.
type EventSession struct {
	Index   int        `json:"index"`
	Title   string     `json:"title,omitempty"`
	StartAt *time.Time `json:"start_at,omitempty"`
	EndAt   *time.Time `json:"end_at,omitempty"`
}

// EventTemplate is the design blob for a badge or certificate.
type EventTemplate struct {
	ImageURL   string         `json:"image_url,omitempty"`
	Background string         `json:"background,omitempty"`
	Fields     map[string]any `json:"fields,omitempty"`
}

// SetIdentity stamps the authoritative id and tenant_id during offline sync,
// overriding whatever the client payload carried (Art. III).
func (e *Event) SetIdentity(id, tenantID string) {
	e.ID = id
	e.TenantID = tenantID
}

// StatusOrDefault returns the persisted status or the borrador default for a
// zero-value Event (e.g. one being built before insert).
func (e *Event) StatusOrDefault() string {
	if e.Status == "" {
		return EventStatusBorrador
	}
	return e.Status
}

func validEventType(t string) bool {
	switch t {
	case EventTypeCurso, EventTypeConferencia, EventTypeHackaton, EventTypeOtro:
		return true
	}
	return false
}

func validEventModality(m string) bool {
	switch m {
	case EventModalityPresencial, EventModalityVirtual, EventModalityHibrido:
		return true
	}
	return false
}

// Validate enforces the business invariants the spec requires before an event
// is persisted (Art. VI input validation, Art. VII money exactness).
func (e *Event) Validate() error {
	if e.TenantID == "" {
		return errors.New("el evento debe pertenecer a un negocio")
	}
	if e.Title == "" {
		return errors.New("el evento necesita un título")
	}
	if !validEventType(e.Type) {
		return errors.New("tipo de evento inválido")
	}
	if !validEventModality(e.Modality) {
		return errors.New("modalidad de evento inválida")
	}
	if e.Currency == "" {
		e.Currency = CurrencyCOP
	}
	if e.Currency != CurrencyCOP && e.Currency != CurrencyUSD {
		return errors.New("moneda inválida")
	}
	if e.Price < 0 {
		return errors.New("el precio no puede ser negativo")
	}
	// La regla del múltiplo de $50 es propia del peso colombiano (Art. VII).
	if e.Currency == CurrencyCOP && e.Price%50 != 0 {
		return errors.New("el precio debe ser múltiplo de $50")
	}
	if e.Capacity < 0 {
		return errors.New("el cupo no puede ser negativo")
	}
	return nil
}
