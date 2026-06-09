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
	// ErrRegistrationNotPaid is returned when the carnet is scanned (or used)
	// before the attendee completed the payment in full (spec FR-09).
	ErrRegistrationNotPaid = errors.New("el asistente aún no ha completado el pago; el carné no es válido")
	// ErrPaymentAmountInvalid is returned for a non-positive payment amount.
	ErrPaymentAmountInvalid = errors.New("el monto del abono debe ser mayor a cero")
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
	reg.AmountPaid = ev.Price
	if err := s.db.Model(&reg).Updates(map[string]any{
		"payment_status": models.RegistrationPaymentConfirmed,
		"amount_paid":    ev.Price,
	}).Error; err != nil {
		return nil, fmt.Errorf("confirmar pago: %w", err)
	}
	return &reg, nil
}

// RecordPayment registers an abono (full or one cuota) of `amount` COP against
// a registration. The running total grows; once it reaches the event price the
// registration is confirmed (consuming a cupo, enforcing capacity). Returns the
// updated registration so the caller can show the new balance / status.
func (s *EventRegistrationService) RecordPayment(tenantID, registrationID string, amount int64) (*models.EventRegistration, error) {
	if amount <= 0 {
		return nil, ErrPaymentAmountInvalid
	}
	var reg models.EventRegistration
	if err := s.db.Where("id = ? AND tenant_id = ?", registrationID, tenantID).First(&reg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRegistrationNotFound
		}
		return nil, err
	}
	if reg.IsConfirmed() {
		return &reg, nil // ya pagó en su totalidad; abono extra es no-op
	}

	var ev models.Event
	if err := s.db.Where("id = ? AND tenant_id = ?", reg.EventID, tenantID).First(&ev).Error; err != nil {
		return nil, err
	}

	reg.AmountPaid += amount
	updates := map[string]any{"amount_paid": reg.AmountPaid}

	// Reaching (or exceeding) the price confirms the inscription and consumes
	// a cupo. assertCapacity guards the cupo invariant before flipping.
	if ev.Price > 0 && reg.AmountPaid >= ev.Price {
		if err := s.assertCapacity(tenantID, &ev); err != nil {
			return nil, err
		}
		reg.PaymentStatus = models.RegistrationPaymentConfirmed
		updates["payment_status"] = models.RegistrationPaymentConfirmed
	}

	if err := s.db.Model(&reg).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("registrar abono: %w", err)
	}
	return &reg, nil
}

// GetByPublicToken resolves a registration by its public token (the unguessable
// id the attendee carries) within a tenant — backs the public carné portal.
func (s *EventRegistrationService) GetByPublicToken(tenantID, token string) (*models.EventRegistration, error) {
	var reg models.EventRegistration
	if err := s.db.Where("public_token = ? AND tenant_id = ?", token, tenantID).First(&reg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRegistrationNotFound
		}
		return nil, err
	}
	return &reg, nil
}

// SubmitProof attaches an attendee's manual-payment proof (transfer/cash
// receipt) to their inscription as a PENDING payment for the organizer to
// review. Resolved by public token (no auth) — the attendee owns the token.
func (s *EventRegistrationService) SubmitProof(tenantID, token string, amount int64, proofURL, note string) (*models.EventPayment, error) {
	if amount <= 0 {
		return nil, ErrPaymentAmountInvalid
	}
	reg, err := s.GetByPublicToken(tenantID, token)
	if err != nil {
		return nil, err
	}
	pay := &models.EventPayment{
		TenantID:       tenantID,
		EventID:        reg.EventID,
		RegistrationID: reg.ID,
		Amount:         amount,
		ProofURL:       proofURL,
		Note:           note,
		Status:         models.EventPaymentPending,
	}
	if err := s.db.Create(pay).Error; err != nil {
		return nil, fmt.Errorf("guardar comprobante: %w", err)
	}
	return pay, nil
}

// ApprovePayment marks a pending payment as approved and feeds its amount into
// the registration's running total (activating the carné when the price is
// reached). Idempotent: an already-approved payment is a no-op.
func (s *EventRegistrationService) ApprovePayment(tenantID, paymentID string) (*models.EventRegistration, error) {
	var pay models.EventPayment
	if err := s.db.Where("id = ? AND tenant_id = ?", paymentID, tenantID).First(&pay).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRegistrationNotFound
		}
		return nil, err
	}

	if pay.Status == models.EventPaymentApproved {
		// Ya aprobado: devuelve la inscripción tal cual (idempotente).
		var reg models.EventRegistration
		if err := s.db.Where("id = ? AND tenant_id = ?", pay.RegistrationID, tenantID).First(&reg).Error; err != nil {
			return nil, ErrRegistrationNotFound
		}
		return &reg, nil
	}

	now := time.Now().UTC()
	if err := s.db.Model(&pay).Updates(map[string]any{
		"status":      models.EventPaymentApproved,
		"reviewed_at": &now,
	}).Error; err != nil {
		return nil, fmt.Errorf("aprobar comprobante: %w", err)
	}
	return s.RecordPayment(tenantID, pay.RegistrationID, pay.Amount)
}

// EventPaymentView is a payment row joined with the attendee's name for the
// organizer's "pagos por revisar" panel.
type EventPaymentView struct {
	ID             string `json:"id"`
	RegistrationID string `json:"registration_id"`
	CustomerName   string `json:"customer_name"`
	Amount         int64  `json:"amount"`
	ProofURL       string `json:"proof_url"`
	Note           string `json:"note"`
	Status         string `json:"status"`
}

// ListPayments returns an event's payments (optionally filtered by status),
// newest first, joined with the attendee name (tenant-scoped, Art. III).
func (s *EventRegistrationService) ListPayments(tenantID, eventID, status string) ([]EventPaymentView, error) {
	q := s.db.Where("tenant_id = ? AND event_id = ?", tenantID, eventID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var pays []models.EventPayment
	if err := q.Order("created_at DESC").Find(&pays).Error; err != nil {
		return nil, err
	}
	out := make([]EventPaymentView, 0, len(pays))
	for _, p := range pays {
		var reg models.EventRegistration
		_ = s.db.Where("id = ?", p.RegistrationID).First(&reg).Error
		var c models.Customer
		_ = s.db.Where("id = ?", reg.CustomerID).First(&c).Error
		out = append(out, EventPaymentView{
			ID:             p.ID,
			RegistrationID: p.RegistrationID,
			CustomerName:   c.Name,
			Amount:         p.Amount,
			ProofURL:       p.ProofURL,
			Note:           p.Note,
			Status:         p.Status,
		})
	}
	return out, nil
}

// EventRegistrationView is one row of the organizer's attendee panel: the
// registration joined with the attendee's contact info and check-in state.
type EventRegistrationView struct {
	ID            string `json:"id"`
	CustomerName  string `json:"customer_name"`
	CustomerPhone string `json:"customer_phone"`
	PaymentMethod string `json:"payment_method"`
	PaymentStatus string `json:"payment_status"`
	AmountPaid    int64  `json:"amount_paid"`
	Price         int64  `json:"price"`
	Balance       int64  `json:"balance"` // price - amount_paid, never negative
	CheckedIn     bool   `json:"checked_in"`
	CheckedOut    bool   `json:"checked_out"`
	CertEligible  bool   `json:"certificate_eligible"`
	CertIssued    bool   `json:"certificate_issued"`
	QRToken       string `json:"qr_token"`
	PublicToken   string `json:"public_token"`
}

// ListByEvent returns the attendee panel for an event (tenant-scoped, Art. III):
// registrations joined with customer name/phone and derived check-in/out flags.
func (s *EventRegistrationService) ListByEvent(tenantID, eventID string) ([]EventRegistrationView, error) {
	var regs []models.EventRegistration
	if err := s.db.Where("tenant_id = ? AND event_id = ?", tenantID, eventID).
		Order("created_at ASC").Find(&regs).Error; err != nil {
		return nil, err
	}

	// Event price once, to derive each attendee's outstanding balance.
	var ev models.Event
	_ = s.db.Where("id = ? AND tenant_id = ?", eventID, tenantID).First(&ev).Error

	out := make([]EventRegistrationView, 0, len(regs))
	for _, r := range regs {
		var c models.Customer
		_ = s.db.Where("id = ? AND tenant_id = ?", r.CustomerID, tenantID).First(&c).Error

		balance := ev.Price - r.AmountPaid
		if balance < 0 {
			balance = 0
		}

		var inN, outN int64
		s.db.Model(&models.EventScan{}).
			Where("registration_id = ? AND tenant_id = ? AND scan_type = ?", r.ID, tenantID, models.ScanTypeIn).
			Count(&inN)
		s.db.Model(&models.EventScan{}).
			Where("registration_id = ? AND tenant_id = ? AND scan_type = ?", r.ID, tenantID, models.ScanTypeOut).
			Count(&outN)

		out = append(out, EventRegistrationView{
			ID:            r.ID,
			CustomerName:  c.Name,
			CustomerPhone: c.Phone,
			PaymentMethod: r.PaymentMethod,
			PaymentStatus: r.PaymentStatus,
			AmountPaid:    r.AmountPaid,
			Price:         ev.Price,
			Balance:       balance,
			CheckedIn:     inN > 0,
			CheckedOut:    outN > 0,
			CertEligible:  r.CertificateEligible,
			CertIssued:    r.CertificateIssuedAt != nil,
			QRToken:       r.QRToken,
			PublicToken:   r.PublicToken,
		})
	}
	return out, nil
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
