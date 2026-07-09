// Spec: specs/101-retocar-fotos-inventario/spec.md
//
// Worker de la cola de retoque (D1-D3 del plan): goroutine ticker in-process
// (instancia única en Render), 1 ítem por tick (~2 llamadas Gemini bajo free
// tier), round-robin por last_served_at entre lotes/tenants (AC-12), backoff
// global persistido ante 429 (AC-10), circuit breaker por lote y recovery de
// huérfanos. El resultado va a retouch_items.candidate_url — products NUNCA
// se toca aquí (FR-05: aplicar es del confirm del tendero).
package services

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// RetouchEnhancer procesa UNA foto por el camino fiel y devuelve la
// candidate URL. Producción: *FaithfulRetouchEnhancer (retouch_enhance.go).
type RetouchEnhancer interface {
	Run(ctx context.Context, tenantID, productID, sourceURL string) (string, error)
}

const (
	// retouchStaleAfter: un processing más viejo que esto es un huérfano
	// (crash/restart) y vuelve a queued.
	retouchStaleAfter = 10 * time.Minute
	// retouchMaxAttempts: fallos no-429 que agotan un ítem → failed.
	retouchMaxAttempts = 3
	// retouchBackoffBase/Cap: pausa global ante 429, exponencial 30s → 10min.
	retouchBackoffBase = 30 * time.Second
	retouchBackoffCap  = 10 * time.Minute
	// retouchBreakerThreshold/Pause: K fallos consecutivos no-429 en un lote
	// → paused_error 5 min (protege al enhance interactivo y al proveedor).
	retouchBreakerThreshold = 5
	retouchBreakerPause     = 5 * time.Minute
	// retouchItemTimeout: presupuesto por foto (mismo criterio que
	// aiJobBackgroundTimeout del flujo individual).
	retouchItemTimeout = 120 * time.Second
	// RetouchDefaultTickSeconds: ritmo por defecto (~1 ítem/12s ≈ 2 llamadas
	// Gemini/ítem bajo free tier). Configurable con RETOUCH_TICK_SECONDS.
	RetouchDefaultTickSeconds = 12
)

// retouchFailedMessage — Art. V: el tendero nunca ve el error crudo.
const retouchFailedMessage = "No pudimos retocar esta foto. Puede reintentarla más tarde."

// retouchStaleReason — razón honesta cuando la foto cambió (plan Art. V).
const (
	retouchStaleReasonChanged = "La foto cambió, no se retocó."
	retouchStaleReasonGone    = "El producto ya no existe."
)

// RetouchWorker drena la cola a ritmo controlado. Estado en memoria: solo el
// nivel de backoff y las rachas del breaker (lo durable — paused_until,
// attempts, estados — vive en la DB y sobrevive reinicios).
type RetouchWorker struct {
	db       *gorm.DB
	enhance  RetouchEnhancer
	interval time.Duration

	// seams de test (mismo paquete): reloj y jitter deterministas.
	now    func() time.Time
	jitter func(time.Duration) time.Duration

	mu          sync.Mutex
	rateLevel   int            // 429 consecutivos → exponente del backoff
	consecFails map[string]int // batchID → fallos no-429 consecutivos

	// restoreOnce deriva rateLevel del paused_until persistido en el primer
	// tick — el backoff sobrevive deploys/restarts en plena racha de 429.
	restoreOnce sync.Once
}

// NewRetouchWorker construye el worker. interval <= 0 cae al default.
func NewRetouchWorker(db *gorm.DB, enhance RetouchEnhancer, interval time.Duration) *RetouchWorker {
	if interval <= 0 {
		interval = RetouchDefaultTickSeconds * time.Second
	}
	return &RetouchWorker{
		db:          db,
		enhance:     enhance,
		interval:    interval,
		now:         time.Now,
		jitter:      defaultRetouchJitter,
		consecFails: map[string]int{},
	}
}

// defaultRetouchJitter suma hasta +25% aleatorio para desincronizar reintentos.
func defaultRetouchJitter(d time.Duration) time.Duration {
	return d + time.Duration(rand.Int63n(int64(d)/4+1))
}

// Start lanza la goroutine del ticker (motor primario; el cron retouch-tick
// es solo backstop). Al arrancar recupera huérfanos del boot anterior.
func (w *RetouchWorker) Start(ctx context.Context) {
	go func() {
		if n, err := w.RecoverStale(); err != nil {
			log.Printf("[retouch] boot recovery: %v", err)
		} else if n > 0 {
			log.Printf("[retouch] boot recovery: %d ítems huérfanos re-encolados", n)
		}
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.Tick(ctx)
			}
		}
	}()
}

// RecoverStale re-encola los processing huérfanos (>10 min sin terminar:
// el proceso murió a mitad de foto). Idempotente; corre en boot, en cada
// tick (barato) y en el backstop del cron.
func (w *RetouchWorker) RecoverStale() (int, error) {
	cutoff := w.now().Add(-retouchStaleAfter)
	res := w.db.Model(&models.RetouchItem{}).
		Where("status = ? AND (started_at IS NULL OR started_at <= ?)",
			models.RetouchItemStatusProcessing, cutoff).
		Updates(map[string]any{
			"status":     models.RetouchItemStatusQueued,
			"started_at": nil,
		})
	return int(res.RowsAffected), res.Error
}

// Backstop es el cuerpo del cron interno retouch-tick: recovery + hasta
// maxItems ítems. Si el ticker in-process murió, esto mantiene la cola viva
// (hasta 30 min congelada, aceptable en background — riesgo bajo del plan).
func (w *RetouchWorker) Backstop(ctx context.Context, maxItems int) (recovered, processed int) {
	// Nil-receiver-safe: main pasa un *RetouchWorker nil (typed nil dentro
	// de la interface del handler) cuando Gemini/R2 no están configurados.
	if w == nil {
		return 0, 0
	}
	recovered, err := w.RecoverStale()
	if err != nil {
		log.Printf("[retouch] backstop recovery: %v", err)
	}
	for i := 0; i < maxItems; i++ {
		if !w.Tick(ctx) {
			break
		}
		processed++
	}
	return recovered, processed
}

// Tick procesa COMO MÁXIMO un ítem: elige el lote menos recientemente
// servido (fairness), reclama atómicamente, verifica staleness de la foto y
// corre el camino fiel. Devuelve true si atendió un ítem (aunque haya
// fallado o salido skipped) — false si no había nada listo para procesar.
func (w *RetouchWorker) Tick(ctx context.Context) bool {
	now := w.now()

	// Restaurar el nivel de backoff heredado (una sola vez, perezoso: acá y
	// no en el constructor para que use el reloj inyectado en tests y corra
	// con las tablas ya migradas).
	w.restoreOnce.Do(func() { w.restoreBackoffLevel(now) })

	// Reanudar lotes en paused_error cuya pausa venció (AC-10: solo).
	w.db.Model(&models.RetouchBatch{}).
		Where("status = ? AND paused_until IS NOT NULL AND paused_until <= ?",
			models.RetouchBatchStatusPausedError, now).
		Updates(map[string]any{"status": models.RetouchBatchStatusRunning, "paused_until": nil})

	// Recovery de huérfanos (barato, idempotente).
	if _, err := w.RecoverStale(); err != nil {
		log.Printf("[retouch] recovery: %v", err)
	}

	batchID := w.pickBatch(now)
	if batchID == "" {
		return false
	}

	// Fairness: marcar servido ANTES de procesar, así el próximo tick
	// atiende a otro tenant aunque este falle.
	w.db.Model(&models.RetouchBatch{}).Where("id = ?", batchID).
		Update("last_served_at", now)

	item, err := w.claimItem(batchID)
	if err != nil {
		log.Printf("[retouch] claim: %v", err)
		return false
	}
	if item == nil {
		return false
	}

	w.safeProcessItem(ctx, batchID, item)
	w.finishBatchIfDrained(batchID)
	return true
}

// safeProcessItem corre processItem con recover(): un panic del enhancer (o
// de cualquier colaborador) JAMÁS tumba el proceso Go — es el POS de todos
// los tenants. Mismo contrato que runAIJob ("never panics the process",
// handlers/ai_jobs.go): el ítem queda failed con el mensaje en español y el
// worker sigue vivo para el próximo tick.
func (w *RetouchWorker) safeProcessItem(ctx context.Context, batchID string, item *models.RetouchItem) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		log.Printf("[retouch-item-panic batch=%s item=%s] %v", batchID, item.ID, r)
		w.db.Model(&models.RetouchItem{}).Where("id = ?", item.ID).
			Updates(map[string]any{
				"status":        models.RetouchItemStatusFailed,
				"error_message": retouchFailedMessage,
			})
	}()
	w.processItem(ctx, batchID, item)
}

// restoreBackoffLevel — MEDIUM 2 review: rateLevel vive en memoria, pero el
// paused_until del 429 queda persistido en los lotes running. Tras un
// restart, derivamos el nivel del tiempo de pausa RESTANTE más largo: una
// pausa vigente de ~30s fue el nivel 0 → el próximo 429 debe escalar a 60s
// (nivel 1), no volver a martillar al proveedor cada 30s. Sin tabla nueva.
func (w *RetouchWorker) restoreBackoffLevel(now time.Time) {
	var stamps []time.Time
	if err := w.db.Model(&models.RetouchBatch{}).
		Where("status = ? AND paused_until IS NOT NULL", models.RetouchBatchStatusRunning).
		Order("paused_until DESC").Limit(1).
		Pluck("paused_until", &stamps).Error; err != nil {
		log.Printf("[retouch] restore backoff: %v", err)
		return
	}
	if len(stamps) == 0 {
		return
	}
	remaining := stamps[0].Sub(now)
	if remaining <= 0 {
		return
	}
	// Nivel cuya pausa cubre lo restante (la pausa persistida de nivel n es
	// base<<n + jitter). Como hay una pausa vigente, al menos un 429 ya
	// ocurrió → el PRÓXIMO usa el nivel siguiente (+1).
	level := 0
	for d := retouchBackoffBase; d < remaining && d < retouchBackoffCap; d <<= 1 {
		level++
	}
	w.mu.Lock()
	if level+1 > w.rateLevel {
		w.rateLevel = level + 1
	}
	w.mu.Unlock()
	log.Printf("[retouch] backoff restaurado tras restart: nivel %d (pausa restante %s)",
		level+1, remaining)
}

// pickBatch elige el lote running no pausado con ítems queued, el menos
// recientemente servido primero (round-robin entre tenants — AC-12).
func (w *RetouchWorker) pickBatch(now time.Time) string {
	var ids []string
	err := w.db.Raw(`
		SELECT b.id FROM retouch_batches b
		WHERE b.status = ? AND b.deleted_at IS NULL
		  AND (b.paused_until IS NULL OR b.paused_until <= ?)
		  AND EXISTS (
			SELECT 1 FROM retouch_items i
			WHERE i.batch_id = b.id AND i.status = ? AND i.deleted_at IS NULL)
		ORDER BY b.last_served_at ASC NULLS FIRST, b.created_at ASC
		LIMIT 1`,
		models.RetouchBatchStatusRunning, now, models.RetouchItemStatusQueued).
		Scan(&ids).Error
	if err != nil {
		log.Printf("[retouch] pick batch: %v", err)
		return ""
	}
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// claimItem reclama atómicamente el ítem queued más antiguo del lote.
// Postgres: UPDATE con subselect FOR UPDATE SKIP LOCKED (dos réplicas jamás
// procesarían el mismo ítem). SQLite (tests): compare-and-set — el UPDATE
// condicionado a status='queued' garantiza que un claim ajeno no se pisa.
func (w *RetouchWorker) claimItem(batchID string) (*models.RetouchItem, error) {
	now := w.now()

	if w.db.Dialector.Name() == "postgres" {
		var claimed []models.RetouchItem
		err := w.db.Raw(`
			UPDATE retouch_items SET status = ?, started_at = ?, updated_at = ?
			WHERE id = (
				SELECT id FROM retouch_items
				WHERE batch_id = ? AND status = ? AND deleted_at IS NULL
				ORDER BY created_at ASC
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			) AND status = ?
			RETURNING *`,
			models.RetouchItemStatusProcessing, now, now,
			batchID, models.RetouchItemStatusQueued,
			models.RetouchItemStatusQueued).
			Scan(&claimed).Error
		if err != nil || len(claimed) == 0 {
			return nil, err
		}
		return &claimed[0], nil
	}

	// Fallback CAS (SQLite en tests; cualquier otro dialecto).
	var cand models.RetouchItem
	if err := w.db.Where("batch_id = ? AND status = ?",
		batchID, models.RetouchItemStatusQueued).
		Order("created_at ASC").First(&cand).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	res := w.db.Model(&models.RetouchItem{}).
		Where("id = ? AND status = ?", cand.ID, models.RetouchItemStatusQueued).
		Updates(map[string]any{
			"status":     models.RetouchItemStatusProcessing,
			"started_at": now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		// Otro worker lo tomó entre el SELECT y el UPDATE — no se pisa.
		return nil, nil
	}
	cand.Status = models.RetouchItemStatusProcessing
	cand.StartedAt = &now
	return &cand, nil
}

// processItem corre el camino fiel para un ítem reclamado y persiste el
// desenlace (ready_for_review / skipped_stale / queued-retry / failed).
func (w *RetouchWorker) processItem(ctx context.Context, batchID string, item *models.RetouchItem) {
	// Idempotencia capa 2: releer el producto — si la foto ACTUAL ya no es
	// el snapshot, o dejó de ser elegible, no se gasta IA (FR-13, spec §9).
	var product models.Product
	if err := w.db.Where("id = ? AND tenant_id = ?", item.ProductID, item.TenantID).
		First(&product).Error; err != nil {
		w.resolveSkippedStale(item, retouchStaleReasonGone)
		return
	}
	if RetouchSourcePhotoURL(product) != item.SourcePhotoURL ||
		!IsProductRetouchEligible(product, item.TenantID) {
		w.resolveSkippedStale(item, retouchStaleReasonChanged)
		return
	}

	itemCtx, cancel := context.WithTimeout(ctx, retouchItemTimeout)
	defer cancel()
	candidateURL, err := w.enhance.Run(itemCtx, item.TenantID, item.ProductID, item.SourcePhotoURL)

	switch {
	case err == nil:
		w.db.Model(&models.RetouchItem{}).Where("id = ?", item.ID).
			Updates(map[string]any{
				"status":        models.RetouchItemStatusReadyForReview,
				"candidate_url": candidateURL,
				"error_message": "",
			})
		w.mu.Lock()
		w.rateLevel = 0
		delete(w.consecFails, batchID)
		w.mu.Unlock()

	case IsRateLimitError(err):
		// AC-10: cuota agotada → pausa GLOBAL persistida (sobrevive
		// reinicios), exponencial con jitter, y el ítem vuelve a la cola
		// SIN gastar attempt (no es culpa de la foto).
		w.pauseAllForRateLimit(batchID, item, err)

	default:
		w.resolveFailure(batchID, item, err)
	}
}

// resolveSkippedStale cierra el ítem sin gastar IA, con razón honesta.
func (w *RetouchWorker) resolveSkippedStale(item *models.RetouchItem, reason string) {
	w.db.Model(&models.RetouchItem{}).Where("id = ?", item.ID).
		Updates(map[string]any{
			"status":        models.RetouchItemStatusSkippedStale,
			"error_message": reason,
		})
}

// pauseAllForRateLimit persiste paused_until en TODOS los lotes activos
// (la cuota del proveedor es compartida) y re-encola el ítem intacto.
func (w *RetouchWorker) pauseAllForRateLimit(batchID string, item *models.RetouchItem, cause error) {
	w.mu.Lock()
	level := w.rateLevel
	w.rateLevel++
	w.mu.Unlock()

	pause := retouchBackoffBase << level
	if pause <= 0 || pause > retouchBackoffCap {
		pause = retouchBackoffCap
	}
	pause = w.jitter(pause)
	if pause > retouchBackoffCap+retouchBackoffCap/4 {
		pause = retouchBackoffCap
	}
	until := w.now().Add(pause)
	log.Printf("[retouch] 429 del proveedor: pausa global %s (nivel %d)", pause, level)

	w.db.Model(&models.RetouchItem{}).Where("id = ?", item.ID).
		Updates(map[string]any{
			"status":     models.RetouchItemStatusQueued,
			"started_at": nil,
		})
	w.db.Model(&models.RetouchBatch{}).
		Where("status = ?", models.RetouchBatchStatusRunning).
		Update("paused_until", until)
	_ = batchID
	_ = cause
}

// resolveFailure registra un fallo no-429: reintenta hasta agotar attempts
// y dispara el circuit breaker del lote a los K fallos consecutivos.
func (w *RetouchWorker) resolveFailure(batchID string, item *models.RetouchItem, cause error) {
	attempts := item.Attempts + 1
	log.Printf("[retouch-item-fail batch=%s item=%s attempt=%d] %v",
		batchID, item.ID, attempts, cause)

	if attempts >= retouchMaxAttempts {
		w.db.Model(&models.RetouchItem{}).Where("id = ?", item.ID).
			Updates(map[string]any{
				"status":        models.RetouchItemStatusFailed,
				"attempts":      attempts,
				"error_message": retouchFailedMessage,
			})
	} else {
		w.db.Model(&models.RetouchItem{}).Where("id = ?", item.ID).
			Updates(map[string]any{
				"status":     models.RetouchItemStatusQueued,
				"started_at": nil,
				"attempts":   attempts,
			})
	}

	w.mu.Lock()
	w.consecFails[batchID]++
	tripped := w.consecFails[batchID] >= retouchBreakerThreshold
	if tripped {
		w.consecFails[batchID] = 0
	}
	w.mu.Unlock()

	if tripped {
		until := w.now().Add(retouchBreakerPause)
		log.Printf("[retouch] breaker: lote %s en paused_error hasta %s", batchID, until)
		w.db.Model(&models.RetouchBatch{}).Where("id = ?", batchID).
			Updates(map[string]any{
				"status":       models.RetouchBatchStatusPausedError,
				"paused_until": until,
			})
	}
}

// finishBatchIfDrained marca completed un lote running sin trabajo activo.
// Los ready_for_review pendientes de revisión NO bloquean el cierre: el
// lote terminó de PROCESAR; revisar es del tendero (FR-14).
func (w *RetouchWorker) finishBatchIfDrained(batchID string) {
	var remaining int64
	if err := w.db.Model(&models.RetouchItem{}).
		Where("batch_id = ? AND status IN ?", batchID,
			[]string{models.RetouchItemStatusQueued, models.RetouchItemStatusProcessing}).
		Count(&remaining).Error; err != nil || remaining > 0 {
		return
	}
	w.db.Model(&models.RetouchBatch{}).
		Where("id = ? AND status = ?", batchID, models.RetouchBatchStatusRunning).
		Update("status", models.RetouchBatchStatusCompleted)
}
