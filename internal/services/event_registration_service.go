// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"vendia-backend/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	// ErrConsentRequired is returned when an attendee tries to register
	// without granting the mandatory communications consent (spec FR-08).
	ErrConsentRequired = errors.New("debe aceptar recibir información del organizador para inscribirse")
	// ErrEventCapacityFull is returned when confirming would exceed the cupo.
	ErrEventCapacityFull = errors.New("cupo agotado para este evento")
	// ErrEventNotPublished is returned when registering to a non-published event.
	ErrEventNotPublished = errors.New("el evento no está disponible para inscripción")
	// ErrRegistrationNotFound is returned for an unknown registration.
	ErrRegistrationNotFound = errors.New("inscripción no encontrada")
)

// RegisterInput carries the public inscription payload.
type RegisterInput struct {
	EventID       string
	ClientID      string // optional client UUID for offline/idempotent create
	Name          string
	Phone         string
	FormData      map[string]any
	ConsentComms  bool
	PaymentMethod string
}

// EventRegistrationService handles attendee inscription, customer dedup,
// consent and the cupo invariant (cupo consumed only on confirmed payment).
type EventRegistrationService struct {
	db *gorm.DB
}

// NewEventRegistrationService wires the service to a GORM handle.
func NewEventRegistrationService(db *gorm.DB) *EventRegistrationService {
	return &EventRegistrationService{db: db}
}

// Register inscribes an attendee. It deduplicates the attendee into the
// organizer's Customer list by phone (spec FR-07), records the mandatory
// consent (FR-08), and sets the payment status: confirmed immediately for a
// free event (price 0), otherwise pending until the payment is confirmed
// (FR-09). The cupo is NOT consumed here for paid events — only at confirm.
func (s *EventRegistrationService) Register(tenantID string, in RegisterInput) (*models.EventRegistration, error) {
	if !in.ConsentComms {
		return nil, ErrConsentRequired
	}

	// Idempotency (Art. II): a re-sent inscription with the same client
	// UUID returns the existing registration instead of duplicating it.
	if in.ClientID != "" && models.IsValidUUID(in.ClientID) {
		var existing models.EventRegistration
		err := s.db.Where("id = ? AND tenant_id = ?", in.ClientID, tenantID).First(&existing).Error
		if err == nil {
			return &existing, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}

	var ev models.Event
	if err := s.db.Where("id = ? AND tenant_id = ?", in.EventID, tenantID).First(&ev).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrEventNotFound
		}
		return nil, err
	}
	if ev.Status != models.EventStatusPublicado {
		return nil, ErrEventNotPublished
	}

	customerID, err := s.upsertCustomer(tenantID, in.Name, in.Phone)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	reg := &models.EventRegistration{
		TenantID:       tenantID,
		EventID:        ev.ID,
		CustomerID:     customerID,
		FormData:       in.FormData,
		ConsentCommsAt: &now,
		PaymentMethod:  in.PaymentMethod,
		PaymentStatus:  models.RegistrationPaymentPending,
		QRToken:        uuid.NewString(),
		PublicToken:    uuid.NewString(),
	}
	if in.ClientID != "" && models.IsValidUUID(in.ClientID) {
		reg.ID = in.ClientID
	}

	// A free event is confirmed at once; it consumes a cupo immediately.
	if ev.Price == 0 {
		if err := s.assertCapacity(tenantID, &ev); err != nil {
			return nil, err
		}
		reg.PaymentStatus = models.RegistrationPaymentConfirmed
	}

	if err := s.db.Create(reg).Error; err != nil {
		return nil, fmt.Errorf("crear inscripción: %w", err)
	}
	return reg, nil
}

// ConfirmPayment moves a pending registration to confirmed, enforcing the
// cupo at the moment of confirmation (decision #7). It is idempotent: an
// already-confirmed registration is returned unchanged.
func (s *EventRegistrationService) ConfirmPayment(tenantID, registrationID string) (*models.EventRegistration, error) {
	var reg models.EventRegistration
	if err := s.db.Where("id = ? AND tenant_id = ?", registrationID, tenantID).First(&reg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRegistrationNotFound
		}
		return nil, err
	}
	if reg.IsConfirmed() {
		return &reg, nil
	}

	var ev models.Event
	if err := s.db.Where("id = ? AND tenant_id = ?", reg.EventID, tenantID).First(&ev).Error; err != nil {
		return nil, err
	}
	if err := s.assertCapacity(tenantID, &ev); err != nil {
		return nil, err
	}

	reg.PaymentStatus = models.RegistrationPaymentConfirmed
	if err := s.db.Model(&reg).Update("payment_status", models.RegistrationPaymentConfirmed).Error; err != nil {
		return nil, fmt.Errorf("confirmar pago: %w", err)
	}
	return &reg, nil
}

// CountConfirmed returns how many confirmed registrations an event has.
func (s *EventRegistrationService) CountConfirmed(tenantID, eventID string) (int64, error) {
	var n int64
	err := s.db.Model(&models.EventRegistration{}).
		Where("tenant_id = ? AND event_id = ? AND payment_status = ?",
			tenantID, eventID, models.RegistrationPaymentConfirmed).
		Count(&n).Error
	return n, err
}

// assertCapacity enforces the cupo invariant. A capacity <= 0 means unlimited.
func (s *EventRegistrationService) assertCapacity(tenantID string, ev *models.Event) error {
	if ev.Capacity <= 0 {
		return nil
	}
	confirmed, err := s.CountConfirmed(tenantID, ev.ID)
	if err != nil {
		return err
	}
	if confirmed >= int64(ev.Capacity) {
		return ErrEventCapacityFull
	}
	return nil
}

// upsertCustomer deduplicates the attendee into the organizer's Customer list
// by normalized phone (mirrors the public-order CRM upsert) and returns the
// Customer ID. Anonymous (no phone) attendees still get a Customer row so the
// registration always has an owner.
func (s *EventRegistrationService) upsertCustomer(tenantID, name, rawPhone string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Asistente"
	}
	phone := NormalizePhone(rawPhone)

	if phone != "" {
		var existing models.Customer
		err := s.db.Where("tenant_id = ? AND phone = ?", tenantID, phone).First(&existing).Error
		if err == nil {
			if name != "Asistente" && name != existing.Name {
				s.db.Model(&existing).Update("name", name)
			}
			return existing.ID, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", err
		}
	}

	customer := models.Customer{TenantID: tenantID, Name: name, Phone: phone}
	if err := s.db.Create(&customer).Error; err != nil {
		return "", fmt.Errorf("crear cliente del asistente: %w", err)
	}
	return customer.ID, nil
}
