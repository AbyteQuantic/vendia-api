// Spec: specs/042-modulo-eventos/spec.md
package database

import (
	"fmt"
	"log"

	"gorm.io/gorm"

	"vendia-backend/internal/models"
)

// BackfillEventSales books any confirmed, PAID event inscription that does not
// yet have a ledger sale (Source="EVENT"), so event money counts in daily sales
// and the financial dashboard like any other sale.
//
// Run-every-boot and idempotent: the NOT EXISTS guard on
// sales.event_registration_id skips a registration already booked. This catches
// (a) inscriptions confirmed BEFORE this feature shipped and (b) the rare case
// where the live best-effort booking failed. Each backfilled sale is stamped
// with the registration's created_at so historical daily totals reflect when
// the money came in.
//
// Migrations (Art. X): Render deploys run GORM AutoMigrate only, never the
// goose .sql files, so this backfill lives in the Go bootstrap. Errors per row
// are logged and skipped — one bad row never crashes the boot.
func BackfillEventSales(db *gorm.DB) (int, error) {
	var regs []models.EventRegistration
	err := db.
		Joins("JOIN events e ON e.id = event_registrations.event_id").
		Where("event_registrations.payment_status = ?", models.RegistrationPaymentConfirmed).
		Where("event_registrations.deleted_at IS NULL").
		Where("e.price > 0 AND e.deleted_at IS NULL").
		Where(`NOT EXISTS (
			SELECT 1 FROM sales s
			WHERE s.event_registration_id = event_registrations.id
			  AND s.deleted_at IS NULL)`).
		Find(&regs).Error
	if err != nil {
		return 0, fmt.Errorf("backfill event sales query: %w", err)
	}

	created := 0
	for i := range regs {
		reg := regs[i]
		var ev models.Event
		if err := db.Where("id = ?", reg.EventID).First(&ev).Error; err != nil {
			continue
		}
		var cust models.Customer
		_ = db.Where("id = ?", reg.CustomerID).First(&cust).Error

		sale := models.BuildEventSale(&reg, &ev, cust.Name, cust.Phone)
		if sale == nil {
			continue
		}
		// Date the revenue to the inscription (GORM keeps a non-zero CreatedAt).
		sale.CreatedAt = reg.CreatedAt
		if err := db.Create(sale).Error; err != nil {
			log.Printf("[BOOTSTRAP] backfill event sale omitida (reg %s): %v", reg.ID, err)
			continue
		}
		created++
	}
	if created > 0 {
		log.Printf("[BOOTSTRAP] backfilled %d ventas de eventos (canal Eventos)", created)
	}
	return created, nil
}
