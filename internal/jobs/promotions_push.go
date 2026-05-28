// Spec: specs/033-difusion-promociones/spec.md
// Spec: specs/038-push-notifications-web-android/spec.md (delivery channel)
package jobs

import (
	"context"
	"fmt"
	"time"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services/push"

	"gorm.io/gorm"
)

// PromotionsPushResult summarises a run of the promotions-push job.
type PromotionsPushResult struct {
	Notified int64 `json:"notified"` // promotions whose owner got a reminder
}

// RunPromotionsPush finds every scheduled promotion whose ScheduledFor
// has arrived and that has not yet had its reminder sent, dispatches a
// push (Spec F038) — que internamente crea también la fila in-app de
// `notifications` (Spec F033 §4.5 #5, AC-06d) — y marca
// SchedulePushSent para no notificar dos veces.
//
// Cuando `dispatcher` es nil, el job degrada al comportamiento pre-F038
// (solo escribe la fila in-app sin push). Esto permite que el cron
// siga funcionando si el sender FCM no está configurado en un entorno.
// (Tomamos `*push.Dispatcher` y NO una interfaz: una interface en Go
// envuelve un puntero nil como un valor NO nil — la comparación
// `if dispatcher == nil` falla y se desreferencia un nil. El struct
// concreto resuelve la ambigüedad en tiempo de compilación.)
func RunPromotionsPush(db *gorm.DB, now time.Time, dispatcher *push.Dispatcher) (PromotionsPushResult, error) {
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

		txErr := dispatchPromotion(db, dispatcher, promo, title, body)
		if txErr != nil {
			// One promotion failing must not abort the rest of the run.
			continue
		}
		result.Notified++
	}

	return result, nil
}

// dispatchPromotion crea la entrada in-app + (opcional) la push, y
// marca `schedule_push_sent` en una sola transacción. Cuando hay
// dispatcher, delega a él (que ya respeta Art. II creando la in-app
// row primero, y FR-13/FR-16/FR-17 con sus reglas). Cuando no hay
// dispatcher, escribe la in-app row directamente — modo degradado.
func dispatchPromotion(db *gorm.DB, dispatcher *push.Dispatcher, promo models.BroadcastPromotion, title, body string) error {
	if dispatcher == nil {
		return db.Transaction(func(tx *gorm.DB) error {
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
	}

	// Con dispatcher: la push y la in-app row van juntas (en el
	// dispatcher), después marcamos `schedule_push_sent`.
	if _, err := dispatcher.DispatchEvent(context.Background(), db, push.Event{
		TenantID: promo.TenantID,
		Type:     "promotion_schedule",
		Title:    title,
		Body:     body,
		DeepLink: "/promociones/" + promo.ID,
		DedupKey: "promo-schedule:" + promo.ID,
	}); err != nil {
		return err
	}
	return db.Model(&models.BroadcastPromotion{}).
		Where("id = ?", promo.ID).
		Update("schedule_push_sent", true).Error
}
