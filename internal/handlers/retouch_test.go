// Spec: specs/101-retocar-fotos-inventario/spec.md
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/database"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	retouchTenantA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	retouchTenantB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func setupRetouchHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Product{}, &models.RetouchBatch{}, &models.RetouchItem{}))
	require.NoError(t, database.ApplyRetouchIndexes(db))
	return db
}

// mountRetouch monta las rutas tenant del flujo de retoque con el tenant
// inyectado (mismo patrón que mountGetAIJob).
func mountRetouch(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if tenantID != "" {
			c.Set(middleware.TenantIDKey, tenantID)
		}
		c.Next()
	})
	r.POST("/inventory/retouch/batches", CreateRetouchBatch(db))
	r.GET("/inventory/retouch/summary", RetouchSummary(db))
	r.POST("/inventory/retouch/items/confirm", ConfirmRetouchItems(db, nil, nil))
	r.POST("/inventory/retouch/items/discard", DiscardRetouchItems(db))
	r.POST("/inventory/retouch/batches/:id/cancel", CancelRetouchBatch(db))
	return r
}

func retouchJSON(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req, _ := http.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// rawProduct crea un producto del tenant con foto propia cruda (elegible).
func rawProduct(t *testing.T, db *gorm.DB, tenantID, name string) models.Product {
	t.Helper()
	p := models.Product{
		TenantID: tenantID, Name: name, Price: 1000,
		PhotoURL: "https://r2.vendia.co/products/" + tenantID + "/" + name + ".jpg",
	}
	require.NoError(t, db.Create(&p).Error)
	return p
}

type retouchBatchResp struct {
	Data struct {
		BatchID     string `json:"batch_id"`
		QueuedCount int    `json:"queued_count"`
		Skipped     []struct {
			ProductID string `json:"product_id"`
			Reason    string `json:"reason"`
		} `json:"skipped"`
	} `json:"data"`
}

func decodeBatchResp(t *testing.T, w *httptest.ResponseRecorder) retouchBatchResp {
	t.Helper()
	var resp retouchBatchResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

// ── T-03 — POST /inventory/retouch/batches ───────────────────────────────

// Cuerpo vacío = "retocar todas": el servidor RECALCULA la elegibilidad
// (AC-01: crudas sí; mejorada/catálogo/muestra/sin foto no).
func TestCreateRetouchBatch_EmptyBodyQueuesAllEligible(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	p1 := rawProduct(t, db, retouchTenantA, "p1")
	p2 := rawProduct(t, db, retouchTenantA, "p2")
	p3 := rawProduct(t, db, retouchTenantA, "p3")
	// Inelegibles: mejorada, catálogo externo, muestra, sin foto.
	require.NoError(t, db.Create(&models.Product{TenantID: retouchTenantA,
		Name: "enh", Price: 1, IsAIEnhanced: true,
		PhotoURL: "https://r2.vendia.co/products/" + retouchTenantA + "/enh-enhanced.jpg"}).Error)
	require.NoError(t, db.Create(&models.Product{TenantID: retouchTenantA,
		Name: "cat", Price: 1, PhotoURL: "https://off.example/x.jpg"}).Error)
	sample := models.Product{TenantID: retouchTenantA, Name: "sam", Price: 1,
		PhotoURL: "https://r2.vendia.co/products/" + retouchTenantA + "/sam.jpg", PhotoIsSample: true}
	require.NoError(t, db.Create(&sample).Error)
	require.NoError(t, db.Create(&models.Product{TenantID: retouchTenantA,
		Name: "nofoto", Price: 1}).Error)
	// Producto de OTRO tenant — jamás entra (AC-08).
	rawProduct(t, db, retouchTenantB, "ajeno")

	r := mountRetouch(db, retouchTenantA)
	w := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/batches", map[string]any{})
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

	resp := decodeBatchResp(t, w)
	assert.NotEmpty(t, resp.Data.BatchID)
	assert.Equal(t, 3, resp.Data.QueuedCount)

	var items []models.RetouchItem
	require.NoError(t, db.Where("batch_id = ?", resp.Data.BatchID).Find(&items).Error)
	require.Len(t, items, 3)
	byProduct := map[string]models.RetouchItem{}
	for _, it := range items {
		byProduct[it.ProductID] = it
		assert.Equal(t, models.RetouchItemStatusQueued, it.Status)
		assert.Equal(t, retouchTenantA, it.TenantID)
	}
	// Snapshot de la foto al encolar (idempotencia capa 2).
	assert.Equal(t, p1.PhotoURL, byProduct[p1.ID].SourcePhotoURL)
	assert.Contains(t, byProduct[p2.ID].SourcePhotoURL, "p2.jpg")
	assert.Contains(t, byProduct[p3.ID].SourcePhotoURL, "p3.jpg")

	var batch models.RetouchBatch
	require.NoError(t, db.First(&batch, "id = ?", resp.Data.BatchID).Error)
	assert.Equal(t, models.RetouchBatchStatusRunning, batch.Status)
	assert.Equal(t, retouchTenantA, batch.TenantID)
	assert.Equal(t, 3, batch.QueuedCount)
}

// IDs explícitos: los ajenos e inelegibles van a skipped[], nunca a la cola.
func TestCreateRetouchBatch_ExplicitIDsSkipForeignAndIneligible(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	mine := rawProduct(t, db, retouchTenantA, "mine")
	foreign := rawProduct(t, db, retouchTenantB, "foreign")
	enhanced := models.Product{TenantID: retouchTenantA, Name: "enh", Price: 1,
		IsAIEnhanced: true,
		PhotoURL:     "https://r2.vendia.co/products/" + retouchTenantA + "/enh.jpg"}
	require.NoError(t, db.Create(&enhanced).Error)

	r := mountRetouch(db, retouchTenantA)
	w := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/batches",
		map[string]any{"product_ids": []string{mine.ID, foreign.ID, enhanced.ID}})
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

	resp := decodeBatchResp(t, w)
	assert.Equal(t, 1, resp.Data.QueuedCount)
	require.Len(t, resp.Data.Skipped, 2)

	var count int64
	db.Model(&models.RetouchItem{}).Where("product_id = ?", foreign.ID).Count(&count)
	assert.Zero(t, count, "un producto ajeno JAMÁS entra a la cola — Art. III")
}

// Con lote activo, el POST AGREGA los elegibles nuevos a ese mismo lote
// (no crea otro, no los pierde): "Mejorar foto" de una tarjeta = lote de 1.
func TestCreateRetouchBatch_ActiveBatchAppendsNewProduct(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	rawProduct(t, db, retouchTenantA, "p1")
	r := mountRetouch(db, retouchTenantA)

	w1 := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/batches", map[string]any{})
	require.Equal(t, http.StatusAccepted, w1.Code)
	first := decodeBatchResp(t, w1)
	require.Equal(t, 1, first.Data.QueuedCount)

	// Llega un producto nuevo mientras el lote corre.
	p2 := rawProduct(t, db, retouchTenantA, "p2")
	w2 := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/batches",
		map[string]any{"product_ids": []string{p2.ID}})
	require.Equal(t, http.StatusAccepted, w2.Code, w2.Body.String())
	second := decodeBatchResp(t, w2)

	assert.Equal(t, first.Data.BatchID, second.Data.BatchID,
		"máx 1 batch activo por tenant: se reutiliza el existente")
	assert.Equal(t, 1, second.Data.QueuedCount, "solo cuenta lo agregado")

	var batchCount int64
	db.Model(&models.RetouchBatch{}).Where("tenant_id = ?", retouchTenantA).Count(&batchCount)
	assert.EqualValues(t, 1, batchCount)

	var batch models.RetouchBatch
	require.NoError(t, db.First(&batch, "id = ?", first.Data.BatchID).Error)
	assert.Equal(t, 2, batch.QueuedCount, "el contador del lote refleja el append")
}

// Re-encolar un producto ya activo en el lote → skipped, sin duplicar (FR-13).
func TestCreateRetouchBatch_AlreadyQueuedProductIsSkipped(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	p1 := rawProduct(t, db, retouchTenantA, "p1")
	r := mountRetouch(db, retouchTenantA)

	w1 := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/batches", map[string]any{})
	require.Equal(t, http.StatusAccepted, w1.Code)
	first := decodeBatchResp(t, w1)

	w2 := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/batches",
		map[string]any{"product_ids": []string{p1.ID}})
	require.Equal(t, http.StatusAccepted, w2.Code)
	second := decodeBatchResp(t, w2)

	assert.Equal(t, first.Data.BatchID, second.Data.BatchID)
	assert.Equal(t, 0, second.Data.QueuedCount)
	require.Len(t, second.Data.Skipped, 1)
	assert.Equal(t, p1.ID, second.Data.Skipped[0].ProductID)

	var count int64
	db.Model(&models.RetouchItem{}).Where("product_id = ?", p1.ID).Count(&count)
	assert.EqualValues(t, 1, count, "AC-11: re-encolar no duplica trabajo")
}

// Sin nada elegible y sin lote activo → 202 sin crear un lote vacío.
func TestCreateRetouchBatch_NothingEligibleCreatesNoBatch(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	r := mountRetouch(db, retouchTenantA)
	w := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/batches", map[string]any{})
	require.Equal(t, http.StatusAccepted, w.Code)
	resp := decodeBatchResp(t, w)
	assert.Empty(t, resp.Data.BatchID)
	assert.Zero(t, resp.Data.QueuedCount)

	var count int64
	db.Model(&models.RetouchBatch{}).Count(&count)
	assert.Zero(t, count)
}

// ── T-03 — cancel ─────────────────────────────────────────────────────────

// Cancelar: queued → canceled; los ready_for_review NO se descartan solos
// (FR-15: lo pendiente se descarta, lo procesado se conserva para revisar).
func TestCancelRetouchBatch_CancelsQueuedKeepsReady(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	batch := models.RetouchBatch{TenantID: retouchTenantA,
		Status: models.RetouchBatchStatusRunning}
	require.NoError(t, db.Create(&batch).Error)
	queued := models.RetouchItem{BatchID: batch.ID, TenantID: retouchTenantA,
		ProductID: "11111111-0000-4000-8000-000000000001",
		Status:    models.RetouchItemStatusQueued}
	ready := models.RetouchItem{BatchID: batch.ID, TenantID: retouchTenantA,
		ProductID:    "11111111-0000-4000-8000-000000000002",
		CandidateURL: "https://r2/cand.jpg",
		Status:       models.RetouchItemStatusReadyForReview}
	require.NoError(t, db.Create(&queued).Error)
	require.NoError(t, db.Create(&ready).Error)

	r := mountRetouch(db, retouchTenantA)
	w := retouchJSON(t, r, http.MethodPost,
		"/inventory/retouch/batches/"+batch.ID+"/cancel", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var b models.RetouchBatch
	require.NoError(t, db.First(&b, "id = ?", batch.ID).Error)
	assert.Equal(t, models.RetouchBatchStatusCanceled, b.Status)

	var q, rd models.RetouchItem
	require.NoError(t, db.First(&q, "id = ?", queued.ID).Error)
	require.NoError(t, db.First(&rd, "id = ?", ready.ID).Error)
	assert.Equal(t, models.RetouchItemStatusCanceled, q.Status)
	assert.Equal(t, models.RetouchItemStatusReadyForReview, rd.Status,
		"lo ya procesado queda pendiente de revisión, no se pierde")
}

func TestCancelRetouchBatch_ForeignTenantIs404(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	batch := models.RetouchBatch{TenantID: retouchTenantB,
		Status: models.RetouchBatchStatusRunning}
	require.NoError(t, db.Create(&batch).Error)

	r := mountRetouch(db, retouchTenantA)
	w := retouchJSON(t, r, http.MethodPost,
		"/inventory/retouch/batches/"+batch.ID+"/cancel", nil)
	assert.Equal(t, http.StatusNotFound, w.Code,
		"un tenant no puede cancelar el lote de otro — Art. III")
}

// ── T-03 — /internal/jobs/retouch-tick: CRON_TOKEN fail-closed ───────────

func mountRetouchTick(w retouchBackstop) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/internal/jobs/retouch-tick", RetouchTickJob(w))
	return r
}

func TestRetouchTickJob_FailsClosedWithoutToken(t *testing.T) {
	t.Setenv("CRON_TOKEN", "")
	r := mountRetouchTick(nil)
	req, _ := http.NewRequest(http.MethodPost, "/internal/jobs/retouch-tick", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"sin CRON_TOKEN configurado el endpoint debe fallar cerrado (503)")
}

func TestRetouchTickJob_RejectsWrongToken(t *testing.T) {
	t.Setenv("CRON_TOKEN", "secreto-correcto")
	r := mountRetouchTick(nil)
	req, _ := http.NewRequest(http.MethodPost, "/internal/jobs/retouch-tick", nil)
	req.Header.Set("Authorization", "Bearer token-equivocado")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

type fakeBackstop struct{ recovered, processed int }

func (f *fakeBackstop) Backstop(ctx context.Context, maxItems int) (int, int) {
	return f.recovered, f.processed
}

func TestRetouchTickJob_RunsBackstopWithValidToken(t *testing.T) {
	t.Setenv("CRON_TOKEN", "secreto-correcto")
	r := mountRetouchTick(&fakeBackstop{recovered: 2, processed: 1})
	req, _ := http.NewRequest(http.MethodPost, "/internal/jobs/retouch-tick", nil)
	req.Header.Set("Authorization", "Bearer secreto-correcto")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Recovered int `json:"recovered"`
		Processed int `json:"processed"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, 2, resp.Recovered)
	assert.Equal(t, 1, resp.Processed)
}

// ── T-09 — confirm / discard / summary ───────────────────────────────────

// seedReadyItem crea producto + lote + ítem ready_for_review con candidata.
func seedReadyItem(t *testing.T, db *gorm.DB, tenantID, name string) (models.Product, models.RetouchBatch, models.RetouchItem) {
	t.Helper()
	p := rawProduct(t, db, tenantID, name)
	batch := models.RetouchBatch{TenantID: tenantID,
		Status: models.RetouchBatchStatusCompleted, QueuedCount: 1, ProcessedCount: 1}
	require.NoError(t, db.Create(&batch).Error)
	item := models.RetouchItem{
		BatchID: batch.ID, TenantID: tenantID, ProductID: p.ID,
		SourcePhotoURL: p.PhotoURL,
		CandidateURL:   "https://r2.vendia.co/products/" + tenantID + "/" + name + "-enhanced.jpg?v=7",
		Status:         models.RetouchItemStatusReadyForReview,
	}
	require.NoError(t, db.Create(&item).Error)
	return p, batch, item
}

// overrideAutoContribute intercepta el seam del aporte automático (098 F2).
func overrideAutoContribute(t *testing.T) *[]string {
	t.Helper()
	var contributed []string
	original := retouchAutoContribute
	retouchAutoContribute = func(db *gorm.DB, catalogSvc *services.CatalogService, geminiSvc *services.GeminiService, tenantID, productID string) {
		contributed = append(contributed, productID)
	}
	t.Cleanup(func() { retouchAutoContribute = original })
	return &contributed
}

// Confirmar aplica candidate→photo_url + is_ai_enhanced y dispara el aporte
// automático SOLO aquí (riesgo alto del plan: jamás en el worker).
func TestConfirmRetouchItems_AppliesCandidateAndAutoContributes(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	p, _, item := seedReadyItem(t, db, retouchTenantA, "p1")
	contributed := overrideAutoContribute(t)

	r := mountRetouch(db, retouchTenantA)
	w := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/items/confirm",
		map[string]any{"item_ids": []string{item.ID}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", p.ID).Error)
	assert.Equal(t, item.CandidateURL, prod.PhotoURL, "la candidata reemplaza la foto")
	assert.True(t, prod.IsAIEnhanced, "FR-10: no vuelve a contar como sin retocar")

	var it models.RetouchItem
	require.NoError(t, db.First(&it, "id = ?", item.ID).Error)
	assert.Equal(t, models.RetouchItemStatusConfirmed, it.Status)

	require.Len(t, *contributed, 1, "autoContribute se dispara en confirm")
	assert.Equal(t, p.ID, (*contributed)[0])
}

// Confirmar dos veces es no-op: no re-aplica ni re-aporta (AC-11, dos
// dispositivos a la vez — la segunda confirmación no rompe nada).
func TestConfirmRetouchItems_IsIdempotent(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	_, _, item := seedReadyItem(t, db, retouchTenantA, "p1")
	contributed := overrideAutoContribute(t)
	r := mountRetouch(db, retouchTenantA)

	w1 := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/items/confirm",
		map[string]any{"item_ids": []string{item.ID}})
	require.Equal(t, http.StatusOK, w1.Code)
	w2 := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/items/confirm",
		map[string]any{"item_ids": []string{item.ID}})
	require.Equal(t, http.StatusOK, w2.Code, "re-confirmar no es error")

	assert.Len(t, *contributed, 1, "el aporte automático no se duplica")
}

// Confirmar un ítem que no está listo (queued) no toca nada.
func TestConfirmRetouchItems_QueuedItemIsSkipped(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	p := rawProduct(t, db, retouchTenantA, "p1")
	batch := models.RetouchBatch{TenantID: retouchTenantA, Status: models.RetouchBatchStatusRunning}
	require.NoError(t, db.Create(&batch).Error)
	item := models.RetouchItem{BatchID: batch.ID, TenantID: retouchTenantA,
		ProductID: p.ID, SourcePhotoURL: p.PhotoURL,
		Status: models.RetouchItemStatusQueued}
	require.NoError(t, db.Create(&item).Error)
	contributed := overrideAutoContribute(t)

	r := mountRetouch(db, retouchTenantA)
	w := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/items/confirm",
		map[string]any{"item_ids": []string{item.ID}})
	require.Equal(t, http.StatusOK, w.Code)

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", p.ID).Error)
	assert.False(t, prod.IsAIEnhanced)
	assert.Empty(t, *contributed)
}

// Art. III: confirmar el ítem de OTRO tenant no aplica nada.
func TestConfirmRetouchItems_ForeignItemIsInvisible(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	p, _, item := seedReadyItem(t, db, retouchTenantB, "ajeno")
	contributed := overrideAutoContribute(t)

	r := mountRetouch(db, retouchTenantA)
	w := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/items/confirm",
		map[string]any{"item_ids": []string{item.ID}})
	require.Equal(t, http.StatusOK, w.Code)

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", p.ID).Error)
	assert.False(t, prod.IsAIEnhanced, "el producto ajeno queda intacto — Art. III")
	assert.Empty(t, *contributed)
}

func TestConfirmRetouchItems_EmptyBodyIs400(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	r := mountRetouch(db, retouchTenantA)
	w := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/items/confirm",
		map[string]any{"item_ids": []string{}})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Descartar deja el producto INTACTO (AC-06) y vuelve a contar como sin
// retocar; jamás dispara el aporte automático.
func TestDiscardRetouchItems_LeavesProductUntouched(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	p, _, item := seedReadyItem(t, db, retouchTenantA, "p1")
	contributed := overrideAutoContribute(t)

	r := mountRetouch(db, retouchTenantA)
	w := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/items/discard",
		map[string]any{"item_ids": []string{item.ID}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", p.ID).Error)
	assert.Equal(t, p.PhotoURL, prod.PhotoURL, "la foto original no cambió — AC-06")
	assert.False(t, prod.IsAIEnhanced)

	var it models.RetouchItem
	require.NoError(t, db.First(&it, "id = ?", item.ID).Error)
	assert.Equal(t, models.RetouchItemStatusDiscarded, it.Status)
	assert.Empty(t, *contributed, "descartar JAMÁS aporta al catálogo")

	// Vuelve a ser elegible: puede re-encolarse (FR-13 sobre activos).
	w2 := retouchJSON(t, r, http.MethodPost, "/inventory/retouch/batches",
		map[string]any{"product_ids": []string{p.ID}})
	require.Equal(t, http.StatusAccepted, w2.Code)
	assert.Equal(t, 1, decodeBatchResp(t, w2).Data.QueuedCount)
}

// ── T-09/T-10 — summary: un solo endpoint de lectura (chip + progreso +
// revisión) ───────────────────────────────────────────────────────────────

func TestRetouchSummary_CountsProgressAndReviewItems(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	// 2 elegibles sueltos + 1 en lote activo (queued) + 1 ready_for_review.
	rawProduct(t, db, retouchTenantA, "libre1")
	rawProduct(t, db, retouchTenantA, "libre2")
	inFlight := rawProduct(t, db, retouchTenantA, "envuelo")
	batch := models.RetouchBatch{TenantID: retouchTenantA,
		Status: models.RetouchBatchStatusRunning, QueuedCount: 2, ProcessedCount: 1}
	require.NoError(t, db.Create(&batch).Error)
	require.NoError(t, db.Create(&models.RetouchItem{BatchID: batch.ID,
		TenantID: retouchTenantA, ProductID: inFlight.ID,
		SourcePhotoURL: inFlight.PhotoURL,
		Status:         models.RetouchItemStatusQueued}).Error)
	ready := rawProduct(t, db, retouchTenantA, "lista")
	readyItem := models.RetouchItem{BatchID: batch.ID, TenantID: retouchTenantA,
		ProductID: ready.ID, SourcePhotoURL: ready.PhotoURL,
		CandidateURL: "https://r2/lista-enhanced.jpg",
		Status:       models.RetouchItemStatusReadyForReview}
	require.NoError(t, db.Create(&readyItem).Error)
	// Ruido de otro tenant (AC-08).
	rawProduct(t, db, retouchTenantB, "ajeno")

	r := mountRetouch(db, retouchTenantA)
	w := retouchJSON(t, r, http.MethodGet, "/inventory/retouch/summary", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			EligibleCount int `json:"eligible_count"`
			ActiveBatch   *struct {
				ID             string `json:"id"`
				Status         string `json:"status"`
				Queued         int    `json:"queued"`
				Processed      int    `json:"processed"`
				Failed         int    `json:"failed"`
				ReadyForReview int    `json:"ready_for_review"`
			} `json:"active_batch"`
			ReviewItems []struct {
				ItemID       string `json:"item_id"`
				ProductID    string `json:"product_id"`
				Name         string `json:"name"`
				OriginalURL  string `json:"original_url"`
				CandidateURL string `json:"candidate_url"`
			} `json:"review_items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, 2, resp.Data.EligibleCount,
		"los productos ya en vuelo no cuentan como 'sin retocar' pendientes")
	require.NotNil(t, resp.Data.ActiveBatch)
	assert.Equal(t, batch.ID, resp.Data.ActiveBatch.ID)
	assert.Equal(t, models.RetouchBatchStatusRunning, resp.Data.ActiveBatch.Status)
	assert.Equal(t, 1, resp.Data.ActiveBatch.Queued)
	assert.Equal(t, 1, resp.Data.ActiveBatch.ReadyForReview)

	require.Len(t, resp.Data.ReviewItems, 1)
	ri := resp.Data.ReviewItems[0]
	assert.Equal(t, readyItem.ID, ri.ItemID)
	assert.Equal(t, ready.ID, ri.ProductID)
	assert.Equal(t, "lista", ri.Name)
	assert.Equal(t, ready.PhotoURL, ri.OriginalURL)
	assert.Equal(t, readyItem.CandidateURL, ri.CandidateURL)
}

func TestRetouchSummary_NoActivityIsCleanZeroState(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	r := mountRetouch(db, retouchTenantA)
	w := retouchJSON(t, r, http.MethodGet, "/inventory/retouch/summary", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			EligibleCount int             `json:"eligible_count"`
			ActiveBatch   json.RawMessage `json:"active_batch"`
			ReviewItems   []any           `json:"review_items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Zero(t, resp.Data.EligibleCount)
	assert.Equal(t, "null", string(resp.Data.ActiveBatch))
	assert.Empty(t, resp.Data.ReviewItems)
}

// AC-09: con el lote ya completed pero fotos por revisar, el summary sigue
// mostrando el lote y sus review_items (el tendero volvió más tarde).
func TestRetouchSummary_CompletedBatchWithPendingReviewStillShows(t *testing.T) {
	db := setupRetouchHandlerDB(t)
	_, batch, readyItem := seedReadyItem(t, db, retouchTenantA, "p1")

	r := mountRetouch(db, retouchTenantA)
	w := retouchJSON(t, r, http.MethodGet, "/inventory/retouch/summary", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			ActiveBatch *struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"active_batch"`
			ReviewItems []struct {
				ItemID string `json:"item_id"`
			} `json:"review_items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data.ActiveBatch)
	assert.Equal(t, batch.ID, resp.Data.ActiveBatch.ID)
	assert.Equal(t, models.RetouchBatchStatusCompleted, resp.Data.ActiveBatch.Status)
	require.Len(t, resp.Data.ReviewItems, 1)
	assert.Equal(t, readyItem.ID, resp.Data.ReviewItems[0].ItemID)
}

func TestRetouchTickJob_NilWorkerIsNoOp(t *testing.T) {
	t.Setenv("CRON_TOKEN", "secreto-correcto")
	r := mountRetouchTick(nil)
	req, _ := http.NewRequest(http.MethodPost, "/internal/jobs/retouch-tick", nil)
	req.Header.Set("Authorization", "Bearer secreto-correcto")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code,
		"sin servicios de IA configurados el tick responde 200 sin procesar")
}
