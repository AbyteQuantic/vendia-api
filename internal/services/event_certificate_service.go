// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"errors"
	"time"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// ErrCertificateNotEligible is returned when issuing a certificate for a
// registration that has not met the attendance/permanence rule.
var ErrCertificateNotEligible = errors.New("el asistente aún no cumple el requisito de permanencia para el certificado")

// EventCertificateService handles manual certificate issuance. The decision
// to issue stays with the organizer (decision #3); this service only enforces
// the eligibility gate and stamps the issuance. The certificate itself is
// served as a web view keyed by the registration's PublicToken (decision #10).
type EventCertificateService struct {
	db *gorm.DB
}

// NewEventCertificateService wires the service to a GORM handle.
func NewEventCertificateService(db *gorm.DB) *EventCertificateService {
	return &EventCertificateService{db: db}
}

// Issue stamps the certificate issuance for an eligible registration. It is
// idempotent — a registration already issued keeps its original timestamp.
func (s *EventCertificateService) Issue(tenantID, registrationID string) (*models.EventRegistration, error) {
	var reg models.EventRegistration
	if err := s.db.Where("id = ? AND tenant_id = ?", registrationID, tenantID).First(&reg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRegistrationNotFound
		}
		return nil, err
	}
	if reg.CertificateIssuedAt != nil {
		return &reg, nil
	}
	if !reg.CertificateEligible {
		return nil, ErrCertificateNotEligible
	}

	now := time.Now().UTC()
	reg.CertificateIssuedAt = &now
	if err := s.db.Model(&reg).Update("certificate_issued_at", now).Error; err != nil {
		return nil, err
	}
	return &reg, nil
}
