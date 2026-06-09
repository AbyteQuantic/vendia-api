// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"errors"
	"time"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EventCheckinService records badge scans (entrada/salida) and keeps each
// registration's certificate eligibility in sync. Scans are idempotent so the
// same QR scanned twice (or synced from two offline devices) never double
// counts (decision R-03, AC-11).
type EventCheckinService struct {
	db *gorm.DB
}

// NewEventCheckinService wires the service to a GORM handle.
func NewEventCheckinService(db *gorm.DB) *EventCheckinService {
	return &EventCheckinService{db: db}
}

// RecordScan registers one scan for the registration identified by its QR
// token. It returns the scan and whether it was newly created (false when the
// scan already existed — "ya registrado"). Eligibility is recomputed after a
// new scan.
func (s *EventCheckinService) RecordScan(tenantID, qrToken, scanType string, sessionIndex int, scannedBy string) (*models.EventScan, bool, error) {
	if scanType != models.ScanTypeIn && scanType != models.ScanTypeOut {
		return nil, false, errors.New("tipo de escaneo inválido")
	}

	var reg models.EventRegistration
	if err := s.db.Where("qr_token = ? AND tenant_id = ?", qrToken, tenantID).First(&reg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrRegistrationNotFound
		}
		return nil, false, err
	}

	// El carné solo es válido cuando el pago está completo (spec FR-09): un
	// asistente con saldo pendiente no puede registrar entrada/salida.
	if !reg.IsConfirmed() {
		return nil, false, ErrRegistrationNotPaid
	}

	scan := &models.EventScan{
		TenantID:       tenantID,
		RegistrationID: reg.ID,
		SessionIndex:   sessionIndex,
		ScanType:       scanType,
		ScannedAt:      time.Now().UTC(),
		ScannedBy:      nullableUUID(scannedBy),
	}
	// OnConflict DoNothing makes a repeated (registration, session, type)
	// scan a no-op rather than an error (Art. II idempotency).
	res := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(scan)
	if res.Error != nil {
		return nil, false, res.Error
	}
	created := res.RowsAffected > 0

	if created {
		if err := s.refreshEligibility(tenantID, &reg); err != nil {
			return nil, created, err
		}
	}
	return scan, created, nil
}

// RecomputeEligibility reloads a registration and refreshes its certificate
// eligibility flag. Used by offline sync after an event_scan is applied.
func (s *EventCheckinService) RecomputeEligibility(tenantID, registrationID string) error {
	var reg models.EventRegistration
	if err := s.db.Where("id = ? AND tenant_id = ?", registrationID, tenantID).First(&reg).Error; err != nil {
		return err
	}
	return s.refreshEligibility(tenantID, &reg)
}

// refreshEligibility recomputes whether a registration meets its event's
// attendance rule and persists the flag.
func (s *EventCheckinService) refreshEligibility(tenantID string, reg *models.EventRegistration) error {
	var ev models.Event
	if err := s.db.Where("id = ? AND tenant_id = ?", reg.EventID, tenantID).First(&ev).Error; err != nil {
		return err
	}

	eligible, err := s.computeEligible(tenantID, reg.ID, &ev)
	if err != nil {
		return err
	}
	if eligible != reg.CertificateEligible {
		reg.CertificateEligible = eligible
		return s.db.Model(reg).Update("certificate_eligible", eligible).Error
	}
	return nil
}

// computeEligible applies the event's attendance rule:
//   - in_out: requires both an entrada and a salida scan on session 0.
//   - pct_sessions: requires an entrada scan on at least AttendancePct% of
//     the event's sessions.
func (s *EventCheckinService) computeEligible(tenantID, registrationID string, ev *models.Event) (bool, error) {
	if ev.AttendanceRule == models.AttendanceRulePctSessions {
		total := len(ev.Sessions)
		if total == 0 {
			total = 1
		}
		var attended int64
		if err := s.db.Model(&models.EventScan{}).
			Where("registration_id = ? AND tenant_id = ? AND scan_type = ?",
				registrationID, tenantID, models.ScanTypeIn).
			Distinct("session_index").Count(&attended).Error; err != nil {
			return false, err
		}
		pct := ev.AttendancePct
		if pct <= 0 {
			pct = 100
		}
		return int(attended)*100 >= pct*total, nil
	}

	// Default in_out rule.
	hasIn, err := s.hasScan(tenantID, registrationID, models.ScanTypeIn)
	if err != nil {
		return false, err
	}
	hasOut, err := s.hasScan(tenantID, registrationID, models.ScanTypeOut)
	if err != nil {
		return false, err
	}
	return hasIn && hasOut, nil
}

// nullableUUID returns nil for an empty string so GORM writes SQL NULL into a
// uuid column instead of ” (Art. X — mirrors middleware.UUIDPtr).
func nullableUUID(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *EventCheckinService) hasScan(tenantID, registrationID, scanType string) (bool, error) {
	var n int64
	err := s.db.Model(&models.EventScan{}).
		Where("registration_id = ? AND tenant_id = ? AND scan_type = ?", registrationID, tenantID, scanType).
		Count(&n).Error
	return n > 0, err
}
