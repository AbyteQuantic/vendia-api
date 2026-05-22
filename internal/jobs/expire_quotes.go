// Spec: specs/031-cotizaciones/spec.md
package jobs

import (
	"time"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// ExpireQuotes moves every `enviada` quote whose valid_until has passed
// into the `vencida` state (Spec F031 AC-10). It is the batch counterpart
// of the lazy per-read expiry in the public quote handler — together they
// form the double safety net described in plan D7.
//
// The update is a single batched UPDATE scoped to the exact predicate
// (status = enviada AND valid_until < now), so it touches only the rows
// that actually need it. Quotes in any other state are left untouched.
// Returns the number of quotes expired.
func ExpireQuotes(db *gorm.DB) (int64, error) {
	res := db.Model(&models.Quote{}).
		Where("status = ? AND valid_until < ?", models.QuoteStatusSent, time.Now().UTC()).
		Update("status", models.QuoteStatusExpired)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
