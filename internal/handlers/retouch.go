// Spec: specs/101-retocar-fotos-inventario/spec.md
//
// Endpoints del retoque masivo de fotos (chip "Fotos sin retocar" + lote en
// segundo plano). El lote SOLO usa el camino FIEL no-generativo (Spec 094):
// el resultado queda en retouch_items.candidate_url sin tocar products hasta
// que el tendero confirma (FR-05). El worker vive en services/retouch_worker.
package handlers

import (
	"context"
	"net/http"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// retouchSkipped es una entrada de skipped[]: el producto que el servidor
// decidió NO encolar y la razón (slug estable para el frontend).
type retouchSkipped struct {
	ProductID string `json:"product_id"`
	Reason    string `json:"reason"`
}

const (
	retouchSkipIneligible = "inelegible" // no existe, es de otro tenant o su foto no es cruda propia
	retouchSkipActive     = "ya_en_lote" // ya tiene un ítem activo (queued/processing/ready_for_review)
)

// retouchActiveBatchStatuses: un lote en estos estados sigue "vivo" — se
// reutiliza en vez de crear otro (máx 1 batch activo por tenant, D3).
var retouchActiveBatchStatuses = []string{
	models.RetouchBatchStatusRunning,
	models.RetouchBatchStatusPausedError,
}

// findActiveRetouchBatch devuelve el lote activo del tenant, si existe.
func findActiveRetouchBatch(db *gorm.DB, tenantID string) (models.RetouchBatch, bool) {
	var batch models.RetouchBatch
	err := db.Where("tenant_id = ? AND status IN ?", tenantID, retouchActiveBatchStatuses).
		Order("created_at DESC").First(&batch).Error
	return batch, err == nil
}

// createActiveRetouchBatch crea el lote running del tenant. Si pierde la
// carrera contra otro request (doble-tap, dueño+empleado a la vez), el
// índice único idx_retouch_batches_active_tenant dispara y devolvemos el
// lote GANADOR — jamás quedan dos activos (D3, y el summary siempre refleja
// el progreso real, AC-09).
func createActiveRetouchBatch(db *gorm.DB, tenantID string) (models.RetouchBatch, error) {
	batch := models.RetouchBatch{TenantID: tenantID, Status: models.RetouchBatchStatusRunning}
	err := db.Create(&batch).Error
	if err == nil {
		return batch, nil
	}
	if services.IsRetouchBatchActiveUniqueViolation(err) {
		if winner, ok := findActiveRetouchBatch(db, tenantID); ok {
			return winner, nil
		}
	}
	return models.RetouchBatch{}, err
}

// CreateRetouchBatch — POST /api/v1/inventory/retouch/batches.
// Body {product_ids?:[]}: vacío = todos los elegibles. El servidor RECALCULA
// la elegibilidad (Art. III — jamás confía en la lista del cliente). Si el
// tenant ya tiene un lote activo, AGREGA los elegibles nuevos a ESE lote
// ("Mejorar foto" de una tarjeta = lote de 1 que se suma al que corre); los
// que ya están activos salen en skipped[] vía el índice UNIQUE parcial.
// 202 {batch_id, queued_count (los agregados en ESTA llamada), skipped[]}.
func CreateRetouchBatch(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req struct {
			ProductIDs []string `json:"product_ids"`
		}
		// Cuerpo vacío o {} es válido (= retocar todas); solo un JSON
		// malformado con contenido es 400.
		if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida"})
			return
		}

		eligible, err := services.EligibleRetouchProducts(db, tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError,
				gin.H{"error": "no se pudo consultar el inventario"})
			return
		}
		eligibleByID := make(map[string]models.Product, len(eligible))
		for _, p := range eligible {
			eligibleByID[p.ID] = p
		}

		// Targets: los pedidos explícitamente (filtrados server-side) o todos.
		skipped := []retouchSkipped{}
		var targets []models.Product
		if len(req.ProductIDs) > 0 {
			for _, id := range req.ProductIDs {
				if p, ok := eligibleByID[id]; ok {
					targets = append(targets, p)
				} else {
					skipped = append(skipped, retouchSkipped{ProductID: id, Reason: retouchSkipIneligible})
				}
			}
		} else {
			targets = eligible
		}

		// Lote activo del tenant (si existe) — se reutiliza SIEMPRE.
		batch, hasBatch := findActiveRetouchBatch(db, tenantID)

		if !hasBatch && len(targets) == 0 {
			// Nada que encolar y nada corriendo: no se crea un lote vacío.
			c.JSON(http.StatusAccepted, gin.H{"data": gin.H{
				"batch_id": "", "queued_count": 0, "skipped": skipped,
			}})
			return
		}
		if !hasBatch {
			var err error
			batch, err = createActiveRetouchBatch(db, tenantID)
			if err != nil {
				c.JSON(http.StatusInternalServerError,
					gin.H{"error": "no se pudo crear el lote de retoque"})
				return
			}
		}

		appended := 0
		for _, p := range targets {
			item := models.RetouchItem{
				BatchID:        batch.ID,
				TenantID:       tenantID,
				ProductID:      p.ID,
				SourcePhotoURL: services.RetouchSourcePhotoURL(p),
				Status:         models.RetouchItemStatusQueued,
			}
			if err := db.Create(&item).Error; err != nil {
				if services.IsRetouchActiveUniqueViolation(err) {
					// Ya vive en un lote activo → idempotente, sin duplicar (FR-13).
					skipped = append(skipped, retouchSkipped{ProductID: p.ID, Reason: retouchSkipActive})
					continue
				}
				c.JSON(http.StatusInternalServerError,
					gin.H{"error": "no se pudo encolar el lote de retoque"})
				return
			}
			appended++
		}

		c.JSON(http.StatusAccepted, gin.H{"data": gin.H{
			"batch_id":     batch.ID,
			"queued_count": appended,
			"skipped":      skipped,
		}})
	}
}

// CancelRetouchBatch — POST /api/v1/inventory/retouch/batches/:id/cancel.
// queued → canceled; el processing en curso termina solo y queda
// ready_for_review; los ready_for_review NO se descartan (FR-15: lo
// procesado se conserva para que el tendero lo revise). Idempotente.
func CancelRetouchBatch(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		batchID := c.Param("id")

		var batch models.RetouchBatch
		if err := db.Where("id = ? AND tenant_id = ?", batchID, tenantID).
			First(&batch).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "lote de retoque no encontrado"})
			return
		}

		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.RetouchItem{}).
				Where("batch_id = ? AND tenant_id = ? AND status = ?",
					batch.ID, tenantID, models.RetouchItemStatusQueued).
				Update("status", models.RetouchItemStatusCanceled).Error; err != nil {
				return err
			}
			return tx.Model(&models.RetouchBatch{}).Where("id = ?", batch.ID).
				Update("status", models.RetouchBatchStatusCanceled).Error
		}); err != nil {
			c.JSON(http.StatusInternalServerError,
				gin.H{"error": "no se pudo cancelar el lote"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Lote cancelado. Lo ya retocado quedó listo para revisar."})
	}
}

// retouchBackstop es lo único que el tick interno necesita del worker —
// recuperar huérfanos y procesar algunos ítems. Interface local (Go: se
// define donde se usa) para que el handler sea testeable sin worker real.
type retouchBackstop interface {
	Backstop(ctx context.Context, maxItems int) (recovered, processed int)
}

// retouchTickMaxItems limita el trabajo por invocación del cron backstop:
// pocas fotos (2 llamadas Gemini c/u) para no colgar el request del cron.
const retouchTickMaxItems = 3

// RetouchTickJob — POST /api/v1/internal/jobs/retouch-tick. Backstop del
// ticker in-process (si la goroutine muere, el cron */30 sigue moviendo la
// cola). Mismo modelo de auth que los demás internal jobs: CRON_TOKEN
// Bearer, fail-closed 503 sin token (patrón internal_jobs.go). Un worker nil
// (servicios de IA no configurados) responde 200 sin procesar.
func RetouchTickJob(w retouchBackstop) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !cronAuthOK(c) {
			return
		}
		if w == nil {
			c.JSON(http.StatusOK, gin.H{"recovered": 0, "processed": 0})
			return
		}
		recovered, processed := w.Backstop(c.Request.Context(), retouchTickMaxItems)
		c.JSON(http.StatusOK, gin.H{"recovered": recovered, "processed": processed})
	}
}

// retouchActiveItemStatuses: un producto con ítem en estos estados está "en
// vuelo" — no cuenta como pendiente en eligible_count ni puede re-encolarse.
var retouchActiveItemStatuses = []string{
	models.RetouchItemStatusQueued,
	models.RetouchItemStatusProcessing,
	models.RetouchItemStatusReadyForReview,
}

// RetouchSummary — GET /api/v1/inventory/retouch/summary. Un solo endpoint
// de lectura (D6): chip (eligible_count), progreso del lote y revisión
// (review_items). eligible_count excluye lo que ya está en vuelo — el chip
// muestra trabajo PENDIENTE; lo encolado se cuenta en active_batch. Si el
// lote terminó de procesar pero quedan fotos por revisar, se sigue
// devolviendo (AC-09: el tendero vuelve más tarde y ve "N listas").
func RetouchSummary(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		eligible, err := services.EligibleRetouchProducts(db, tenantID)
		if err != nil {
			c.JSON(http.StatusInternalServerError,
				gin.H{"error": "no se pudo consultar el inventario"})
			return
		}
		// Productos con ítem activo no cuentan como pendientes.
		var inFlight []string
		db.Model(&models.RetouchItem{}).
			Where("tenant_id = ? AND status IN ?", tenantID, retouchActiveItemStatuses).
			Pluck("product_id", &inFlight)
		inFlightSet := make(map[string]bool, len(inFlight))
		for _, id := range inFlight {
			inFlightSet[id] = true
		}
		eligibleCount := 0
		for _, p := range eligible {
			if !inFlightSet[p.ID] {
				eligibleCount++
			}
		}

		// Lote a mostrar: el activo (running/paused_error) o, si no hay, el
		// más reciente con fotos aún por revisar.
		batch, hasBatch := findActiveRetouchBatch(db, tenantID)
		if !hasBatch {
			hasBatch = db.Where(`tenant_id = ? AND status != ? AND id IN (
				SELECT batch_id FROM retouch_items
				WHERE tenant_id = ? AND status = ? AND deleted_at IS NULL)`,
				tenantID, models.RetouchBatchStatusCanceled,
				tenantID, models.RetouchItemStatusReadyForReview).
				Order("created_at DESC").First(&batch).Error == nil
		}

		var activeBatch gin.H
		if hasBatch {
			counts := map[string]int64{}
			var rows []struct {
				Status string
				N      int64
			}
			db.Model(&models.RetouchItem{}).Select("status, COUNT(*) AS n").
				Where("batch_id = ?", batch.ID).Group("status").Scan(&rows)
			for _, r := range rows {
				counts[r.Status] = r.N
			}
			activeBatch = gin.H{
				"id":     batch.ID,
				"status": batch.Status,
				// queued = trabajo aún por procesar (incluye el que corre).
				"queued": counts[models.RetouchItemStatusQueued] +
					counts[models.RetouchItemStatusProcessing],
				// processed = todo lo que la IA ya resolvió.
				"processed": counts[models.RetouchItemStatusReadyForReview] +
					counts[models.RetouchItemStatusConfirmed] +
					counts[models.RetouchItemStatusDiscarded] +
					counts[models.RetouchItemStatusSkippedStale],
				"failed":           counts[models.RetouchItemStatusFailed],
				"ready_for_review": counts[models.RetouchItemStatusReadyForReview],
			}
		}

		// Revisión: TODAS las fotos listas del tenant (de cualquier lote no
		// cancelado), con nombre del producto, paginadas (inventarios 500+).
		p := parsePagination(c)
		type reviewRow struct {
			ItemID       string `json:"item_id"`
			ProductID    string `json:"product_id"`
			Name         string `json:"name"`
			OriginalURL  string `json:"original_url"`
			CandidateURL string `json:"candidate_url"`
		}
		reviewItems := []reviewRow{}
		if err := db.Table("retouch_items").
			Select(`retouch_items.id AS item_id, retouch_items.product_id,
				products.name, retouch_items.source_photo_url AS original_url,
				retouch_items.candidate_url`).
			Joins("JOIN products ON products.id = retouch_items.product_id").
			Where("retouch_items.tenant_id = ? AND retouch_items.status = ? AND retouch_items.deleted_at IS NULL",
				tenantID, models.RetouchItemStatusReadyForReview).
			Order("retouch_items.updated_at ASC").
			Limit(p.PerPage).Offset((p.Page - 1) * p.PerPage).
			Scan(&reviewItems).Error; err != nil {
			c.JSON(http.StatusInternalServerError,
				gin.H{"error": "no se pudo consultar las fotos por revisar"})
			return
		}

		data := gin.H{
			"eligible_count": eligibleCount,
			"review_items":   reviewItems,
		}
		if hasBatch {
			data["active_batch"] = activeBatch
		} else {
			data["active_batch"] = nil
		}
		c.JSON(http.StatusOK, gin.H{"data": data})
	}
}

// retouchAutoContribute es el seam del aporte automático al catálogo
// (Spec 098 F2). Variable para que los tests verifiquen que se dispara SOLO
// en confirm — riesgo alto del plan: jamás con fotos luego descartadas.
var retouchAutoContribute = autoContributePhoto

// ConfirmRetouchItems — POST /api/v1/inventory/retouch/items/confirm.
// {item_ids:[]} aplica candidate→photo_url + is_ai_enhanced=true y dispara
// autoContributePhoto. Idempotente: un ítem ya confirmado es no-op. Sirve
// para 1 foto o para "Aplicar las N" (D6). Los ítems no listos (o ajenos)
// van a skipped[] sin tocar nada.
func ConfirmRetouchItems(db *gorm.DB, catalogSvc *services.CatalogService, geminiSvc *services.GeminiService) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var req struct {
			ItemIDs []string `json:"item_ids"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || len(req.ItemIDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "debe indicar qué fotos confirmar"})
			return
		}

		// LOTE de verdad (MEDIUM 3): 1 SELECT de todos los ítems del tenant,
		// clasificación en memoria, updates de ítems agrupados. El UPDATE de
		// products sí es por ítem (cada candidata es distinta) — eso está
		// bien; lo que no escala son 2-3 round-trips de ítems por id.
		var items []models.RetouchItem
		if err := db.Where("tenant_id = ? AND id IN ?", tenantID, req.ItemIDs).
			Find(&items).Error; err != nil {
			c.JSON(http.StatusInternalServerError,
				gin.H{"error": "no se pudo consultar las fotos"})
			return
		}
		byID := make(map[string]models.RetouchItem, len(items))
		for _, it := range items {
			byID[it.ID] = it
		}

		confirmed := 0
		skipped := []string{}
		seen := map[string]bool{}
		var toApply []models.RetouchItem // en el orden del request
		for _, itemID := range req.ItemIDs {
			if seen[itemID] {
				continue
			}
			seen[itemID] = true
			item, ok := byID[itemID]
			if !ok {
				// Inexistente o de otro tenant — invisible (Art. III).
				skipped = append(skipped, itemID)
				continue
			}
			switch item.Status {
			case models.RetouchItemStatusConfirmed:
				// Ya aplicado (otro dispositivo ganó) → no-op idempotente.
				confirmed++
			case models.RetouchItemStatusReadyForReview:
				toApply = append(toApply, item)
			default:
				skipped = append(skipped, itemID)
			}
		}

		var confirmedIDs, staleIDs []string
		var contributeProducts []string
		for _, item := range toApply {
			res := db.Model(&models.Product{}).
				Where("id = ? AND tenant_id = ?", item.ProductID, tenantID).
				Updates(map[string]any{
					"photo_url":      item.CandidateURL,
					"is_ai_enhanced": true,
				})
			if res.Error != nil {
				c.JSON(http.StatusInternalServerError,
					gin.H{"error": "no se pudo aplicar la foto retocada"})
				return
			}
			if res.RowsAffected == 0 {
				// El producto se borró mientras la foto esperaba revisión
				// (spec §9): el ítem se cierra sin romper el flujo.
				staleIDs = append(staleIDs, item.ID)
				skipped = append(skipped, item.ID)
				continue
			}
			confirmedIDs = append(confirmedIDs, item.ID)
			contributeProducts = append(contributeProducts, item.ProductID)
		}

		if len(confirmedIDs) > 0 {
			if err := db.Model(&models.RetouchItem{}).
				Where("id IN ? AND status = ?", confirmedIDs,
					models.RetouchItemStatusReadyForReview).
				Update("status", models.RetouchItemStatusConfirmed).Error; err != nil {
				c.JSON(http.StatusInternalServerError,
					gin.H{"error": "no se pudo confirmar las fotos"})
				return
			}
			confirmed += len(confirmedIDs)
		}
		if len(staleIDs) > 0 {
			db.Model(&models.RetouchItem{}).Where("id IN ?", staleIDs).
				Updates(map[string]any{
					"status":        models.RetouchItemStatusSkippedStale,
					"error_message": "El producto ya no existe.",
				})
		}
		// Aporte automático (098 F2) — SOLO al confirmar, por ítem
		// confirmado y en el orden del request.
		for _, productID := range contributeProducts {
			retouchAutoContribute(db, catalogSvc, geminiSvc, tenantID, productID)
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"confirmed": confirmed,
			"skipped":   skipped,
		}})
	}
}

// DiscardRetouchItems — POST /api/v1/inventory/retouch/items/discard.
// {item_ids:[]} descarta candidatas: el producto queda INTACTO (AC-06) y
// vuelve a contar como sin retocar. La candidata -enhanced huérfana en R2 se
// queda (política del plan: es barata). Idempotente.
func DiscardRetouchItems(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var req struct {
			ItemIDs []string `json:"item_ids"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || len(req.ItemIDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "debe indicar qué fotos descartar"})
			return
		}

		// LOTE (MEDIUM 3): 1 UPDATE agrupado + 1 SELECT de verificación —
		// nunca una query por ítem.
		if err := db.Model(&models.RetouchItem{}).
			Where("tenant_id = ? AND id IN ? AND status = ?",
				tenantID, req.ItemIDs, models.RetouchItemStatusReadyForReview).
			Update("status", models.RetouchItemStatusDiscarded).Error; err != nil {
			c.JSON(http.StatusInternalServerError,
				gin.H{"error": "no se pudo descartar la foto"})
			return
		}

		// Estado final de los ids pedidos: discarded (recién o de antes —
		// idempotente) cuenta; el resto (otro estado, ajeno o inexistente)
		// sale en skipped por diferencia de conjuntos.
		var rows []struct {
			ID     string
			Status string
		}
		if err := db.Model(&models.RetouchItem{}).
			Select("id, status").
			Where("tenant_id = ? AND id IN ?", tenantID, req.ItemIDs).
			Scan(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError,
				gin.H{"error": "no se pudo verificar las fotos descartadas"})
			return
		}
		statusByID := make(map[string]string, len(rows))
		for _, r := range rows {
			statusByID[r.ID] = r.Status
		}

		discarded := 0
		skipped := []string{}
		seen := map[string]bool{}
		for _, itemID := range req.ItemIDs {
			if seen[itemID] {
				continue
			}
			seen[itemID] = true
			if statusByID[itemID] == models.RetouchItemStatusDiscarded {
				discarded++
			} else {
				skipped = append(skipped, itemID)
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"discarded": discarded,
			"skipped":   skipped,
		}})
	}
}
