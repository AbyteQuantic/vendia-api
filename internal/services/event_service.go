// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"errors"
	"fmt"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// ErrEventNotFound is returned when an event does not exist for the tenant.
var ErrEventNotFound = errors.New("evento no encontrado")

// EventService owns the lifecycle of an organizer's events (Spec F042).
// Every method is scoped by tenant_id (Art. III) — an event is never read or
// mutated across tenants.
type EventService struct {
	db *gorm.DB
}

// NewEventService wires the service to a GORM handle.
func NewEventService(db *gorm.DB) *EventService {
	return &EventService{db: db}
}

// Create validates and persists a new event for the tenant. The status
// defaults to borrador; the client-provided UUID (if any) is honored for
// offline idempotency (Art. II).
func (s *EventService) Create(tenantID string, e *models.Event) (*models.Event, error) {
	e.TenantID = tenantID
	if e.Status == "" {
		e.Status = models.EventStatusBorrador
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if err := s.db.Create(e).Error; err != nil {
		return nil, fmt.Errorf("crear evento: %w", err)
	}
	return e, nil
}

// Get loads one event scoped to the tenant.
func (s *EventService) Get(tenantID, id string) (*models.Event, error) {
	var e models.Event
	if err := s.db.Where("id = ? AND tenant_id = ?", id, tenantID).First(&e).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrEventNotFound
		}
		return nil, err
	}
	return &e, nil
}

// List returns the tenant's events, optionally filtered by status, newest
// first.
func (s *EventService) List(tenantID, status string) ([]models.Event, error) {
	q := s.db.Where("tenant_id = ?", tenantID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var events []models.Event
	if err := q.Order("created_at DESC").Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

// Update applies whitelisted mutable fields to an event, re-validating the
// result. It returns the updated event.
func (s *EventService) Update(tenantID, id string, patch *models.Event) (*models.Event, error) {
	e, err := s.Get(tenantID, id)
	if err != nil {
		return nil, err
	}
	applyEventPatch(e, patch)
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if err := s.db.Save(e).Error; err != nil {
		return nil, fmt.Errorf("actualizar evento: %w", err)
	}
	return e, nil
}

// Publish validates the event and moves it to publicado so it surfaces in the
// public catalog.
func (s *EventService) Publish(tenantID, id string) (*models.Event, error) {
	return s.setStatus(tenantID, id, models.EventStatusPublicado, true)
}

// Archive hides the event from the catalog without deleting its data.
func (s *EventService) Archive(tenantID, id string) (*models.Event, error) {
	return s.setStatus(tenantID, id, models.EventStatusArchivado, false)
}

func (s *EventService) setStatus(tenantID, id, status string, validate bool) (*models.Event, error) {
	e, err := s.Get(tenantID, id)
	if err != nil {
		return nil, err
	}
	if validate {
		if err := e.Validate(); err != nil {
			return nil, err
		}
	}
	e.Status = status
	if err := s.db.Model(e).Update("status", status).Error; err != nil {
		return nil, fmt.Errorf("cambiar estado del evento: %w", err)
	}
	return e, nil
}

// applyEventPatch copies the mutable fields a tendero can edit. Identity and
// money-state fields (TenantID, status) are managed by dedicated methods.
func applyEventPatch(dst, src *models.Event) {
	if src.Type != "" {
		dst.Type = src.Type
	}
	if src.Title != "" {
		dst.Title = src.Title
	}
	dst.Description = src.Description
	if src.Modality != "" {
		dst.Modality = src.Modality
	}
	dst.LocationOrLink = src.LocationOrLink
	dst.StartAt = src.StartAt
	dst.EndAt = src.EndAt
	dst.Capacity = src.Capacity
	dst.Price = src.Price
	if len(src.EnabledPaymentMethods) > 0 {
		dst.EnabledPaymentMethods = src.EnabledPaymentMethods
	}
	dst.InstallmentsEnabled = src.InstallmentsEnabled
	dst.InstallmentsCount = src.InstallmentsCount
	if len(src.CustomFields) > 0 {
		dst.CustomFields = src.CustomFields
	}
	if len(src.Sessions) > 0 {
		dst.Sessions = src.Sessions
	}
	if src.AttendanceRule != "" {
		dst.AttendanceRule = src.AttendanceRule
	}
	if src.AttendancePct != 0 {
		dst.AttendancePct = src.AttendancePct
	}
}
