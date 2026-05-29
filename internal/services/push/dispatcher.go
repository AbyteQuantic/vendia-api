// Spec: specs/038-push-notifications-web-android/spec.md
//
// Dispatcher orquesta las reglas que todos los triggers de push
// (web_order, promo, fiado, stock, admin manual) comparten:
//
//   1. ¿Está el tenant suspendido (PRO_PAST_DUE) o eliminado? → skip.
//   2. ¿Hay dedup_key reciente (≤ 5 min) ya pusheado? → skip.
//   3. Crear la fila in-app en `notifications` (Art. II offline-first).
//   4. ¿Ya alcanzó el cap diario (20)? → no push, in-app sí queda.
//   5. Cargar tokens activos del tenant (filtro multi-tenant — Art. III).
//   6. Enviar al sender; recoger tokens inválidos.
//   7. Marcar `pushed_at` en la notification (si al menos 1 token OK).
//   8. Setear `invalidated_at` en cada token reportado muerto.
//
// El orden importa: la fila in-app se crea ANTES del envío para no
// perder el evento si el push falla — Art. II offline-first. El cap
// se chequea DESPUÉS del in-app porque AC-15 dice "el evento se
// registra en notifications pero NO se envía push".
package push

import (
	"context"
	"errors"
	"fmt"
	"time"

	"vendia-backend/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Event describe lo que cualquiera de los 5 triggers (4 automáticos +
// 1 admin manual) le pasa al dispatcher. Es inmutable.
type Event struct {
	TenantID string
	Type     string // "web_order" | "credit_payment" | "credit_close" | "promo" | "stock_low" | "admin_manual"
	Title    string
	Body     string
	DeepLink string // opcional; si no vacío, el cliente navega al tap
	DedupKey string // opcional; si vacío, no se aplica dedup
}

// Outcome resume qué pasó. Permite al caller loguear el resultado y
// al admin broadcast reportar `tokens_targeted` / `tokens_invalidated`.
type Outcome struct {
	Status         string
	NotificationID string
	TokensSent     int
	TokensInvalid  int
}

// Outcome.Status — los 5 estados terminales del dispatcher.
const (
	OutcomeSent             = "sent"               // in-app + push OK
	OutcomeCapReached       = "cap_reached"        // in-app sí, push no (FR-16)
	OutcomeSkippedDuplicate = "skipped_duplicate"  // dedup hit (FR-13)
	OutcomeSkippedSuspended = "skipped_suspended"  // tenant moroso (FR-17)
	OutcomeNoTokens         = "no_tokens"          // in-app sí, no había a quién mandar
)

// Constantes de comportamiento — el spec las fija; el plan permite
// ajustarlas tras observar producción.
const (
	dedupWindow  = 5 * time.Minute
	dailyPushCap = 20
)

// Dispatcher mantiene la inyección del sender + un reloj override
// para tests (clock fake).
type Dispatcher struct {
	sender Sender
	nowFn  func() time.Time
}

// NewDispatcher fija las dependencias mínimas. El sender se inyecta
// desde main.go (FCMSender en producción, FakeSender en tests).
func NewDispatcher(sender Sender) *Dispatcher {
	if sender == nil {
		panic("push.NewDispatcher: sender no puede ser nil")
	}
	return &Dispatcher{
		sender: sender,
		nowFn:  time.Now,
	}
}

// DispatchEvent es el único punto de entrada. Idempotencia bajo el
// dedup_key — los reintentos del sender o del trigger no duplican
// notificaciones.
func (d *Dispatcher) DispatchEvent(ctx context.Context, db *gorm.DB, evt Event) (Outcome, error) {
	if evt.TenantID == "" {
		return Outcome{}, errors.New("push: TenantID requerido en Event")
	}
	if evt.Title == "" {
		return Outcome{}, errors.New("push: Title requerido en Event")
	}

	now := d.nowFn()

	// Paso 1 — tenant suspendido (FR-17, AC-16).
	eligible, err := isTenantPushEligible(db, evt.TenantID)
	if err != nil {
		return Outcome{}, fmt.Errorf("push: chequeo de elegibilidad del tenant: %w", err)
	}
	if !eligible {
		return Outcome{Status: OutcomeSkippedSuspended}, nil
	}

	// Paso 2 — dedup_key dentro de ventana (FR-13, AC-13).
	if evt.DedupKey != "" {
		var count int64
		windowStart := now.Add(-dedupWindow)
		if err := db.Model(&models.Notification{}).
			Where("tenant_id = ? AND dedup_key = ? AND pushed_at IS NOT NULL AND pushed_at >= ?",
				evt.TenantID, evt.DedupKey, windowStart).
			Count(&count).Error; err != nil {
			return Outcome{}, fmt.Errorf("push: chequeo de dedup: %w", err)
		}
		if count > 0 {
			return Outcome{Status: OutcomeSkippedDuplicate}, nil
		}
	}

	// Paso 3 — crear fila in-app primero (Art. II).
	notifID := uuid.NewString()
	notif := buildNotification(notifID, evt, now)
	if err := db.Create(&notif).Error; err != nil {
		return Outcome{}, fmt.Errorf("push: insertando notification in-app: %w", err)
	}

	// Paso 4 — chequear cap diario (FR-16, AC-15). Se cuenta SOBRE
	// las notifications previas (NO incluye la recién creada porque
	// todavía no tiene pushed_at).
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)
	var pushedToday int64
	if err := db.Model(&models.Notification{}).
		Where("tenant_id = ? AND pushed_at >= ? AND pushed_at < ?",
			evt.TenantID, startOfDay, endOfDay).
		Count(&pushedToday).Error; err != nil {
		return Outcome{}, fmt.Errorf("push: contando cap diario: %w", err)
	}
	if pushedToday >= dailyPushCap {
		return Outcome{Status: OutcomeCapReached, NotificationID: notifID}, nil
	}

	// Paso 5 — cargar tokens activos del tenant.
	tokens, err := loadActiveTokens(db, evt.TenantID)
	if err != nil {
		return Outcome{}, fmt.Errorf("push: cargando tokens: %w", err)
	}
	if len(tokens) == 0 {
		return Outcome{Status: OutcomeNoTokens, NotificationID: notifID}, nil
	}

	// Paso 6 — construir Targets (FCM o Web Push según la fila) y enviar.
	targets := make([]Target, 0, len(tokens))
	for _, t := range tokens {
		tgt := Target{DeviceID: t.ID}
		if t.IsWebPush() {
			tgt.Endpoint = *t.Endpoint
			tgt.P256dh = *t.P256dh
			tgt.Auth = *t.Auth
		} else {
			tgt.FCMToken = t.Token
		}
		targets = append(targets, tgt)
	}
	result, err := d.sender.Send(ctx, targets, Payload{
		Title:    evt.Title,
		Body:     evt.Body,
		DeepLink: evt.DeepLink,
	})
	if err != nil {
		// Error transitorio del sender — la notification in-app ya
		// quedó; el reintento del trigger lo volverá a tomar (el
		// dedup lo protege si vuelve dentro de 5 min). Propagamos
		// para que el caller logue.
		return Outcome{NotificationID: notifID}, fmt.Errorf("push: sender: %w", err)
	}

	// Paso 7 — marcar pushed_at si alguien recibió.
	if result.Sent > 0 {
		nowPtr := now
		if err := db.Model(&models.Notification{}).
			Where("id = ?", notifID).
			Update("pushed_at", &nowPtr).Error; err != nil {
			return Outcome{}, fmt.Errorf("push: marcando pushed_at: %w", err)
		}
	}

	// Paso 8 — invalidar tokens muertos (por device_id, portable
	// entre protocolos — el sender reporta ids, no tokens).
	if len(result.Invalid) > 0 {
		invalidatedAt := now
		if err := db.Model(&models.DeviceToken{}).
			Where("tenant_id = ? AND id IN ?", evt.TenantID, result.Invalid).
			Update("invalidated_at", &invalidatedAt).Error; err != nil {
			return Outcome{}, fmt.Errorf("push: invalidando tokens muertos: %w", err)
		}
	}

	return Outcome{
		Status:         OutcomeSent,
		NotificationID: notifID,
		TokensSent:     result.Sent,
		TokensInvalid:  len(result.Invalid),
	}, nil
}

// buildNotification ensambla la fila in-app a partir del evento.
// Centralizado acá para que el dispatcher sea la única fuente de
// verdad sobre cómo se traduce un Event a una Notification.
func buildNotification(id string, evt Event, now time.Time) models.Notification {
	n := models.Notification{
		ID:        id,
		CreatedAt: now,
		TenantID:  evt.TenantID,
		Title:     evt.Title,
		Body:      evt.Body,
		Type:      evt.Type,
	}
	if evt.DeepLink != "" {
		dl := evt.DeepLink
		n.DeepLink = &dl
	}
	if evt.DedupKey != "" {
		dk := evt.DedupKey
		n.DedupKey = &dk
	}
	return n
}

// isTenantPushEligible aplica FR-17: tenant soft-deleted o con
// suscripción PRO_PAST_DUE no recibe push. TRIAL y FREE sí reciben
// (no son "suspendidos por morosidad", son tenants normales en su
// ciclo). Devuelve `true` si el tenant pasa, `false` si no.
//
// Nota: el filtro es defensivo — usa Model+Where en vez de SELECT
// crudo para que el GORM soft-delete del tenant (`deleted_at`) se
// aplique automáticamente.
func isTenantPushEligible(db *gorm.DB, tenantID string) (bool, error) {
	var tenant models.Tenant
	if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil // tenant no existe (o soft-deleted) → no envía
		}
		return false, err
	}

	var sub models.TenantSubscription
	err := db.Where("tenant_id = ?", tenantID).First(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Sin suscripción explícita asumimos elegible (tenants viejos
		// pre-F008 podrían no tenerla; el bootstrap los rellena).
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if sub.Status == models.SubscriptionStatusProPastDue {
		return false, nil
	}
	return true, nil
}

// loadActiveTokens carga los tokens NO invalidados del tenant. El
// filtro de tenant_id es OBLIGATORIO (Art. III) — no hay query de
// tokens en el dispatcher que lo omita.
func loadActiveTokens(db *gorm.DB, tenantID string) ([]models.DeviceToken, error) {
	var tokens []models.DeviceToken
	err := db.Where("tenant_id = ? AND invalidated_at IS NULL", tenantID).
		Find(&tokens).Error
	return tokens, err
}
