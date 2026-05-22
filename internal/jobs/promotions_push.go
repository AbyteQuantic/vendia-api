// Spec: specs/033-difusion-promociones/spec.md
package jobs

import (
	"fmt"
	"time"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// PromotionsPushResult summarises a run of the promotions-push job.
type PromotionsPushResult struct {
	Notified int64 `json:"notified"` // promotions whose owner got a reminder
}

// RunPromotionsPush finds every scheduled promotion whose ScheduledFor
// has arrived and that has not yet had its reminder sent, notifies the
// owner, and marks SchedulePushSent so the reminder never fires twice
// (Spec F033 §4.5 #5, AC-06d).
//
// VendIA has no FCM infrastructure yet, so the "push" is a Notification
// row — the same in-app mechanism the quotes module uses to surface
// owner-facing events. When real FCM lands the delivery channel can be
// swapped here without touching the scheduling logic.
//
// Each promotion is handled in its own transaction: a notification write
// and the schedule_push_sent flip succeed or fail together, so a crash
// mid-run never leaves a promotion notified-but-unmarked (which would
// double-notify on the next tick).
func RunPromotionsPush(db *gorm.DB, now time.Time) (PromotionsPushResult, error) {
	var due []models.BroadcastPromotion
	if err := db.
		Where("scheduled_for IS NOT NULL AND scheduled_for <= ? AND schedule_push_sent = ?",
			now, false).
		Find(&due).Error; err != nil {
		return PromotionsPushResult{}, fmt.Errorf("error al buscar promociones programadas: %w", err)
	}

	var result PromotionsPushResult
	for _, promo := range due {
		// Audience size for the reminder copy — best-effort; a count
		// error just yields a reminder without the number.
		var audience int64
		db.Model(&models.BroadcastPromotionDelivery{}).
			Where("promotion_id = ?", promo.ID).
			Count(&audience)

		title := "Es hora de enviar tu promoción"
		body := fmt.Sprintf(
			"Tu promoción \"%s\" está lista para enviar. Abre VendIA y arranca la cola de WhatsApp.",
			promo.Title)
		if audience > 0 {
			body = fmt.Sprintf(
				"Tu promoción \"%s\" está lista para enviar a %d cliente(s). Abre VendIA y arranca la cola.",
				promo.Title, audience)
		}

		txErr := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&models.Notification{
				TenantID: promo.TenantID,
				Type:     "promotion_schedule",
				Title:    title,
				Body:     body,
			}).Error; err != nil {
				return err
			}
			return tx.Model(&models.BroadcastPromotion{}).
				Where("id = ?", promo.ID).
				Update("schedule_push_sent", true).Error
		})
		if txErr != nil {
			// One promotion failing must not abort the rest of the run.
			continue
		}
		result.Notified++
	}

	return result, nil
}
