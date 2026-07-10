// Spec: specs/101-retocar-fotos-inventario/spec.md
package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// tinyJPEG genera una foto real mínima (8x8) para el camino fiel.
func tinyJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

// tinyMaskPNG genera una máscara B/N válida (todo producto = blanco).
func tinyMaskPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// fakeSegmenter implementa ProductSegmenter Y ADEMÁS expone los métodos
// generativos (EnhancePhoto/StudioShot) para demostrar que el camino del
// lote jamás los toca — riesgo #1 del plan.
type fakeSegmenter struct {
	mask    []byte
	maskErr error
	deg     float64
	degErr  error

	segCalls        int
	rotCalls        int
	generativeCalls int
}

func (f *fakeSegmenter) SegmentProductMask(ctx context.Context, imageData []byte, mimeType string) ([]byte, error) {
	f.segCalls++
	return f.mask, f.maskErr
}

func (f *fakeSegmenter) EstimateUprightRotation(ctx context.Context, imageData []byte, mimeType string) (float64, error) {
	f.rotCalls++
	return f.deg, f.degErr
}

// Métodos GENERATIVOS — el lote no debe poder llamarlos nunca.
func (f *fakeSegmenter) EnhancePhoto(ctx context.Context, imageData []byte, mimeType, productInfo, instruction string) ([]byte, error) {
	f.generativeCalls++
	return nil, errors.New("camino generativo prohibido en el lote")
}

func (f *fakeSegmenter) StudioShot(ctx context.Context, imageData []byte, mimeType, productInfo string) ([]byte, error) {
	f.generativeCalls++
	return nil, errors.New("camino generativo prohibido en el lote")
}

// memStorage es un FileStorage en memoria.
type memStorage struct {
	uploads  map[string][]byte
	mimes    map[string]string
	lastKey  string
	uploadEr error
}

func newMemStorage() *memStorage {
	return &memStorage{uploads: map[string][]byte{}, mimes: map[string]string{}}
}

func (m *memStorage) Upload(ctx context.Context, bucket, key string, data []byte, contentType string) (string, error) {
	if m.uploadEr != nil {
		return "", m.uploadEr
	}
	m.uploads[key] = data
	m.mimes[key] = contentType
	m.lastKey = key
	return "https://r2.vendia.co/" + key, nil
}

func (m *memStorage) Download(ctx context.Context, bucket, key string) ([]byte, string, error) {
	return m.uploads[key], m.mimes[key], nil
}

func (m *memStorage) Delete(ctx context.Context, bucket, key string) error { return nil }

func fakeDownload(data []byte) func(ctx context.Context, url string) ([]byte, string, error) {
	return func(ctx context.Context, url string) ([]byte, string, error) {
		return data, "image/jpeg", nil
	}
}

func testEnhanceDeps(t *testing.T, seg *fakeSegmenter, st *memStorage) FaithfulEnhanceDeps {
	t.Helper()
	return FaithfulEnhanceDeps{
		Gemini:   seg,
		Storage:  st,
		Download: fakeDownload(tinyJPEG(t)),
		Now:      func() time.Time { return time.Unix(0, 424242) },
	}
}

// ── T-05 — RunFaithfulEnhance (extracción del camino FIEL) ───────────────

func TestRunFaithfulEnhance_UploadsEnhancedKeyWithCacheBust(t *testing.T) {
	seg := &fakeSegmenter{mask: tinyMaskPNG(t), deg: 0}
	st := newMemStorage()
	url, err := RunFaithfulEnhance(context.Background(), testEnhanceDeps(t, seg, st),
		"tenant-1", "prod-1", "https://r2.vendia.co/products/tenant-1/prod-1.jpg")
	require.NoError(t, err)

	// Misma clave determinista + cache-bust del flujo individual (Spec 094).
	assert.Contains(t, url, "products/tenant-1/prod-1-enhanced.jpg")
	assert.Contains(t, url, "?v=424242")
	assert.Equal(t, "products/tenant-1/prod-1-enhanced.jpg", st.lastKey)
	assert.Equal(t, "image/jpeg", st.mimes[st.lastKey])
	assert.NotEmpty(t, st.uploads[st.lastKey])

	assert.Equal(t, 1, seg.segCalls, "máscara: exactamente 1 llamada")
	assert.Equal(t, 1, seg.rotCalls, "rotación: exactamente 1 llamada")
}

// Fail-safe Spec 094: si la máscara falla, el resultado es solo el realce de
// la foto original — nunca un error, nunca un producto alterado.
func TestRunFaithfulEnhance_MaskFailureIsFailSafe(t *testing.T) {
	seg := &fakeSegmenter{maskErr: errors.New("gemini API returned 500")}
	st := newMemStorage()
	url, err := RunFaithfulEnhance(context.Background(), testEnhanceDeps(t, seg, st),
		"tenant-1", "prod-1", "https://r2.vendia.co/products/tenant-1/prod-1.jpg")
	require.NoError(t, err, "máscara caída → fail-safe, no error")
	assert.Contains(t, url, "-enhanced.jpg")
}

// Rotación caída → fail-safe 0° (mismo comportamiento que uprightRotation).
func TestRunFaithfulEnhance_RotationFailureIsFailSafe(t *testing.T) {
	seg := &fakeSegmenter{mask: tinyMaskPNG(t), degErr: errors.New("timeout")}
	st := newMemStorage()
	_, err := RunFaithfulEnhance(context.Background(), testEnhanceDeps(t, seg, st),
		"tenant-1", "prod-1", "https://x/p.jpg")
	assert.NoError(t, err)
}

func TestRunFaithfulEnhance_DownloadErrorPropagates(t *testing.T) {
	seg := &fakeSegmenter{}
	st := newMemStorage()
	deps := testEnhanceDeps(t, seg, st)
	deps.Download = func(ctx context.Context, url string) ([]byte, string, error) {
		return nil, "", fmt.Errorf("error al obtener foto: %w", errors.New("conn refused"))
	}
	_, err := RunFaithfulEnhance(context.Background(), deps, "t", "p", "https://x/p.jpg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "error al obtener foto",
		"el wrap debe seguir siendo clasificable (download_failed)")
	assert.Zero(t, seg.segCalls, "sin foto no se llama a Gemini")
}

func TestRunFaithfulEnhance_UploadErrorIsClassifiable(t *testing.T) {
	seg := &fakeSegmenter{mask: tinyMaskPNG(t)}
	st := newMemStorage()
	st.uploadEr = errors.New("r2 down")
	_, err := RunFaithfulEnhance(context.Background(), testEnhanceDeps(t, seg, st),
		"t", "p", "https://x/p.jpg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "error al guardar foto",
		"mismo wrap que el flujo individual (upload_failed)")
}

// Riesgo #1 del plan: el lote JAMÁS usa el camino generativo. El enhancer
// del lote se construye SOLO sobre ProductSegmenter (máscara + rotación) —
// no existe parámetro mode — y nunca invoca EnhancePhoto/StudioShot.
func TestFaithfulRetouchEnhancer_NeverCallsGenerativePath(t *testing.T) {
	seg := &fakeSegmenter{mask: tinyMaskPNG(t)}
	st := newMemStorage()
	enhance := NewFaithfulRetouchEnhancer(seg, st)
	enhance.Download = fakeDownload(tinyJPEG(t))

	url, err := enhance.Run(context.Background(), "tenant-1", "prod-9", "https://x/p.jpg")
	require.NoError(t, err)
	assert.Contains(t, url, "prod-9-enhanced.jpg")
	assert.Zero(t, seg.generativeCalls,
		"el lote no puede tocar EnhancePhoto/StudioShot (generativos)")
	assert.Positive(t, seg.segCalls)
}

// El clasificador compartido de 429 (backoff AC-10) reconoce los patrones
// reales del proveedor.
func TestIsRateLimitError_SharedPatterns(t *testing.T) {
	assert.True(t, IsRateLimitError(errors.New("gemini API returned 429")))
	assert.True(t, IsRateLimitError(errors.New("RESOURCE_EXHAUSTED: quota")))
	assert.True(t, IsRateLimitError(errors.New("Too Many Requests")))
	assert.False(t, IsRateLimitError(errors.New("gemini API returned 500")))
	assert.False(t, IsRateLimitError(nil))
}

// ── T-07 — worker: claim, fairness, staleness, backoff, breaker ──────────

// funcEnhancer adapta una función a RetouchEnhancer.
type funcEnhancer func(ctx context.Context, tenantID, productID, sourceURL string) (string, error)

func (f funcEnhancer) Run(ctx context.Context, tenantID, productID, sourceURL string) (string, error) {
	return f(ctx, tenantID, productID, sourceURL)
}

// okEnhancer siempre produce una candidata determinista.
func okEnhancer() funcEnhancer {
	return func(ctx context.Context, tenantID, productID, sourceURL string) (string, error) {
		return "https://r2.vendia.co/products/" + tenantID + "/" + productID + "-enhanced.jpg?v=1", nil
	}
}

// newTestWorker arma un worker con reloj y jitter deterministas.
func newTestWorker(t *testing.T, db *gorm.DB, enhance RetouchEnhancer) (*RetouchWorker, *time.Time) {
	t.Helper()
	w := NewRetouchWorker(db, enhance, time.Second)
	current := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return current }
	w.jitter = func(d time.Duration) time.Duration { return d }
	return w, &current
}

func seedBatchWithItems(t *testing.T, db *gorm.DB, tenantID string, n int) (models.RetouchBatch, []models.RetouchItem, []models.Product) {
	t.Helper()
	batch := models.RetouchBatch{TenantID: tenantID,
		Status: models.RetouchBatchStatusRunning}
	require.NoError(t, db.Create(&batch).Error)
	items := make([]models.RetouchItem, 0, n)
	products := make([]models.Product, 0, n)
	base := time.Date(2026, 7, 8, 11, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		p := models.Product{TenantID: tenantID, Name: fmt.Sprintf("p%d", i), Price: 1000,
			PhotoURL: fmt.Sprintf("https://r2.vendia.co/products/%s/p%d.jpg", tenantID, i)}
		require.NoError(t, db.Create(&p).Error)
		it := models.RetouchItem{
			BaseModel:      models.BaseModel{CreatedAt: base.Add(time.Duration(i) * time.Second)},
			BatchID:        batch.ID,
			TenantID:       tenantID,
			ProductID:      p.ID,
			SourcePhotoURL: p.PhotoURL,
			Status:         models.RetouchItemStatusQueued,
		}
		require.NoError(t, db.Create(&it).Error)
		items = append(items, it)
		products = append(products, p)
	}
	return batch, items, products
}

func reloadItem(t *testing.T, db *gorm.DB, id string) models.RetouchItem {
	t.Helper()
	var it models.RetouchItem
	require.NoError(t, db.First(&it, "id = ?", id).Error)
	return it
}

func reloadBatch(t *testing.T, db *gorm.DB, id string) models.RetouchBatch {
	t.Helper()
	var b models.RetouchBatch
	require.NoError(t, db.First(&b, "id = ?", id).Error)
	return b
}

// El corazón del lote: procesar deja el resultado en candidate_url y NO toca
// el producto (FR-05) — nada cambia en la tienda sin confirmar.
func TestRetouchWorker_TickStoresCandidateWithoutTouchingProduct(t *testing.T) {
	db := setupRetouchDB(t)
	batch, items, products := seedBatchWithItems(t, db, retouchTestTenantA, 1)
	w, _ := newTestWorker(t, db, okEnhancer())

	assert.True(t, w.Tick(context.Background()))

	it := reloadItem(t, db, items[0].ID)
	assert.Equal(t, models.RetouchItemStatusReadyForReview, it.Status)
	assert.Contains(t, it.CandidateURL, "-enhanced.jpg")

	var p models.Product
	require.NoError(t, db.First(&p, "id = ?", products[0].ID).Error)
	assert.Equal(t, products[0].PhotoURL, p.PhotoURL, "products NO se toca hasta confirmar")
	assert.False(t, p.IsAIEnhanced)

	b := reloadBatch(t, db, batch.ID)
	assert.Equal(t, models.RetouchBatchStatusCompleted, b.Status,
		"sin queued/processing restantes el lote queda completed")
}

// Claim atómico (compare-and-set): cada ítem se reclama exactamente una vez;
// un ítem ya tomado por otro worker no se reclama doble.
func TestRetouchWorker_ClaimIsAtomic(t *testing.T) {
	db := setupRetouchDB(t)
	batch, items, _ := seedBatchWithItems(t, db, retouchTestTenantA, 3)
	w, _ := newTestWorker(t, db, okEnhancer())

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		it, err := w.claimItem(batch.ID)
		require.NoError(t, err)
		require.NotNil(t, it)
		assert.False(t, seen[it.ID], "un ítem no puede reclamarse dos veces")
		seen[it.ID] = true
		assert.Equal(t, models.RetouchItemStatusProcessing, reloadItem(t, db, it.ID).Status)
	}
	// Cola vacía → nil sin error.
	it, err := w.claimItem(batch.ID)
	require.NoError(t, err)
	assert.Nil(t, it)

	// La carrera pura: alguien más marcó processing entre el SELECT y el
	// UPDATE → el CAS (WHERE status='queued') no pisa el claim ajeno.
	require.NoError(t, db.Model(&models.RetouchItem{}).
		Where("id = ?", items[0].ID).
		Update("status", models.RetouchItemStatusProcessing).Error)
	it, err = w.claimItem(batch.ID)
	require.NoError(t, err)
	assert.Nil(t, it)
}

// AC-12: dos tiendas con lotes activos avanzan intercaladas (round-robin por
// last_served_at) — la de 8 fotos no espera a la de 500.
func TestRetouchWorker_RoundRobinBetweenTenants(t *testing.T) {
	db := setupRetouchDB(t)
	_, _, _ = seedBatchWithItems(t, db, retouchTestTenantA, 3)
	_, _, _ = seedBatchWithItems(t, db, retouchTestTenantB, 3)

	var served []string
	enhance := funcEnhancer(func(ctx context.Context, tenantID, productID, sourceURL string) (string, error) {
		served = append(served, tenantID)
		return "https://r2/c.jpg", nil
	})
	w, current := newTestWorker(t, db, enhance)

	for i := 0; i < 4; i++ {
		require.True(t, w.Tick(context.Background()), "tick %d", i)
		*current = current.Add(time.Second)
	}
	require.Len(t, served, 4)
	countA, countB := 0, 0
	for _, tid := range served {
		if tid == retouchTestTenantA {
			countA++
		} else {
			countB++
		}
	}
	assert.Equal(t, 2, countA, "equidad: A avanza intercalado")
	assert.Equal(t, 2, countB, "equidad: B avanza intercalado")
	assert.NotEqual(t, served[0], served[1], "el segundo tick atiende al OTRO tenant")
}

// Recovery: processing huérfano >10 min (crash/restart) vuelve a queued;
// uno reciente se respeta (puede estar corriendo de verdad).
func TestRetouchWorker_RecoverStaleProcessing(t *testing.T) {
	db := setupRetouchDB(t)
	batch, items, _ := seedBatchWithItems(t, db, retouchTestTenantA, 2)
	w, current := newTestWorker(t, db, okEnhancer())

	old := current.Add(-11 * time.Minute)
	recent := current.Add(-2 * time.Minute)
	require.NoError(t, db.Model(&models.RetouchItem{}).Where("id = ?", items[0].ID).
		Updates(map[string]any{"status": models.RetouchItemStatusProcessing, "started_at": old}).Error)
	require.NoError(t, db.Model(&models.RetouchItem{}).Where("id = ?", items[1].ID).
		Updates(map[string]any{"status": models.RetouchItemStatusProcessing, "started_at": recent}).Error)

	recovered, err := w.RecoverStale()
	require.NoError(t, err)
	assert.Equal(t, 1, recovered)
	assert.Equal(t, models.RetouchItemStatusQueued, reloadItem(t, db, items[0].ID).Status)
	assert.Equal(t, models.RetouchItemStatusProcessing, reloadItem(t, db, items[1].ID).Status)
	_ = batch
}

// Idempotencia capa 2 (FR-13, spec §9): si la foto ACTUAL ya no es la del
// snapshot (o el producto se volvió inelegible / se borró), el ítem sale
// como skipped_stale con razón honesta y la IA no se llama.
func TestRetouchWorker_StalePhotoIsSkippedWithoutAICall(t *testing.T) {
	db := setupRetouchDB(t)
	batch, items, products := seedBatchWithItems(t, db, retouchTestTenantA, 2)

	// p0: el tendero cambió la foto después de encolar.
	require.NoError(t, db.Model(&models.Product{}).Where("id = ?", products[0].ID).
		Update("photo_url", "https://r2.vendia.co/products/"+retouchTestTenantA+"/nueva.jpg").Error)
	// p1: el producto ya no existe.
	require.NoError(t, db.Delete(&models.Product{}, "id = ?", products[1].ID).Error)

	calls := 0
	enhance := funcEnhancer(func(ctx context.Context, tenantID, productID, sourceURL string) (string, error) {
		calls++
		return "https://r2/c.jpg", nil
	})
	w, _ := newTestWorker(t, db, enhance)
	require.True(t, w.Tick(context.Background()))
	require.True(t, w.Tick(context.Background()))

	assert.Zero(t, calls, "foto vieja/producto borrado no gastan llamadas de IA")
	it0 := reloadItem(t, db, items[0].ID)
	assert.Equal(t, models.RetouchItemStatusSkippedStale, it0.Status)
	assert.Contains(t, it0.ErrorMessage, "La foto cambió", "razón honesta en español")
	it1 := reloadItem(t, db, items[1].ID)
	assert.Equal(t, models.RetouchItemStatusSkippedStale, it1.Status)

	b := reloadBatch(t, db, batch.ID)
	assert.Equal(t, models.RetouchBatchStatusCompleted, b.Status)
}

// AC-10: 429 del proveedor pausa GLOBALMENTE (paused_until persistido en los
// lotes activos), re-encola el ítem SIN gastar attempt, y crece exponencial.
func TestRetouchWorker_RateLimitPausesGloballyAndRequeues(t *testing.T) {
	db := setupRetouchDB(t)
	batchA, itemsA, _ := seedBatchWithItems(t, db, retouchTestTenantA, 1)
	batchB, _, _ := seedBatchWithItems(t, db, retouchTestTenantB, 1)

	enhance := funcEnhancer(func(ctx context.Context, tenantID, productID, sourceURL string) (string, error) {
		return "", errors.New("gemini API returned 429")
	})
	w, current := newTestWorker(t, db, enhance)

	require.True(t, w.Tick(context.Background()))

	it := reloadItem(t, db, itemsA[0].ID)
	assert.Equal(t, models.RetouchItemStatusQueued, it.Status, "429 re-encola el ítem")
	assert.Zero(t, it.Attempts, "429 NO gasta attempt")

	bA := reloadBatch(t, db, batchA.ID)
	bB := reloadBatch(t, db, batchB.ID)
	require.NotNil(t, bA.PausedUntil)
	require.NotNil(t, bB.PausedUntil, "la pausa por cuota es global: afecta a TODOS los lotes activos")
	assert.Equal(t, current.Add(30*time.Second).Unix(), bA.PausedUntil.Unix(),
		"primer 429 → pausa base 30s")

	// Mientras dura la pausa no se procesa nada.
	assert.False(t, w.Tick(context.Background()))

	// Pasada la pausa, reintenta; segundo 429 consecutivo → 60s (exponencial).
	*current = current.Add(31 * time.Second)
	require.True(t, w.Tick(context.Background()))
	bA = reloadBatch(t, db, batchA.ID)
	require.NotNil(t, bA.PausedUntil)
	assert.Equal(t, current.Add(60*time.Second).Unix(), bA.PausedUntil.Unix(),
		"backoff exponencial 30s→60s")
}

// MEDIUM 2 review: el nivel de backoff sobrevive un deploy/restart en plena
// racha de 429 — el worker nuevo lo deriva del paused_until persistido en
// vez de arrancar en 0 (y volver a martillar al proveedor cada 30s).
func TestRetouchWorker_BackoffLevelSurvivesRestart(t *testing.T) {
	db := setupRetouchDB(t)
	batch, _, _ := seedBatchWithItems(t, db, retouchTestTenantA, 1)

	enhance := funcEnhancer(func(ctx context.Context, tenantID, productID, sourceURL string) (string, error) {
		return "", errors.New("gemini API returned 429")
	})
	w, current := newTestWorker(t, db, enhance)

	// Estado heredado del proceso anterior: primer 429 → pausa base 30s.
	pausedUntil := current.Add(30 * time.Second)
	require.NoError(t, db.Model(&models.RetouchBatch{}).Where("id = ?", batch.ID).
		Update("paused_until", pausedUntil).Error)

	// "Restart": worker recién construido. Mientras la pausa rige, nada corre.
	assert.False(t, w.Tick(context.Background()))

	// Vencida la pausa, el siguiente 429 debe escalar a 60s (nivel 1), no
	// resetear a 30s como si fuera el primero.
	*current = current.Add(31 * time.Second)
	require.True(t, w.Tick(context.Background()))
	b := reloadBatch(t, db, batch.ID)
	require.NotNil(t, b.PausedUntil)
	assert.Equal(t, current.Add(60*time.Second).Unix(), b.PausedUntil.Unix(),
		"tras restart el backoff continúa la escalera (60s), no vuelve a 30s")
}

// Tras un restart con una pausa LARGA vigente (racha avanzada), el nivel
// derivado es proporcional — p. ej. ~4min restantes → siguiente 429 ≈ 8min.
func TestRetouchWorker_BackoffRestoreScalesWithRemainingPause(t *testing.T) {
	db := setupRetouchDB(t)
	batch, _, _ := seedBatchWithItems(t, db, retouchTestTenantA, 1)
	enhance := funcEnhancer(func(ctx context.Context, tenantID, productID, sourceURL string) (string, error) {
		return "", errors.New("rate limit")
	})
	w, current := newTestWorker(t, db, enhance)

	pausedUntil := current.Add(4 * time.Minute) // pausa de nivel 3 (30s<<3)
	require.NoError(t, db.Model(&models.RetouchBatch{}).Where("id = ?", batch.ID).
		Update("paused_until", pausedUntil).Error)

	assert.False(t, w.Tick(context.Background()))
	*current = current.Add(4*time.Minute + time.Second)
	require.True(t, w.Tick(context.Background()))

	b := reloadBatch(t, db, batch.ID)
	require.NotNil(t, b.PausedUntil)
	assert.Equal(t, current.Add(8*time.Minute).Unix(), b.PausedUntil.Unix(),
		"pausa vigente ~4min = nivel 3 → el siguiente 429 pausa 8min (nivel 4)")
}

func TestRetouchWorker_BackoffIsCappedAt10Minutes(t *testing.T) {
	db := setupRetouchDB(t)
	seedBatchWithItems(t, db, retouchTestTenantA, 1)
	enhance := funcEnhancer(func(ctx context.Context, tenantID, productID, sourceURL string) (string, error) {
		return "", errors.New("rate limit exceeded")
	})
	w, current := newTestWorker(t, db, enhance)
	w.rateLevel = 12 // muy por encima del cap

	require.True(t, w.Tick(context.Background()))
	var b models.RetouchBatch
	require.NoError(t, db.Where("tenant_id = ?", retouchTestTenantA).First(&b).Error)
	require.NotNil(t, b.PausedUntil)
	assert.LessOrEqual(t, b.PausedUntil.Sub(*current), 10*time.Minute, "cap 10 min")
}

// Circuit breaker: 5 fallos consecutivos no-429 → paused_error 5 min. Un
// ítem que agota 3 attempts queda failed con mensaje en español.
func TestRetouchWorker_ConsecutiveFailuresTripBreaker(t *testing.T) {
	db := setupRetouchDB(t)
	batch, items, _ := seedBatchWithItems(t, db, retouchTestTenantA, 2)
	enhance := funcEnhancer(func(ctx context.Context, tenantID, productID, sourceURL string) (string, error) {
		return "", errors.New("gemini upstream 500")
	})
	w, current := newTestWorker(t, db, enhance)

	for i := 0; i < 5; i++ {
		require.True(t, w.Tick(context.Background()), "tick %d", i)
		*current = current.Add(time.Second)
	}

	b := reloadBatch(t, db, batch.ID)
	assert.Equal(t, models.RetouchBatchStatusPausedError, b.Status,
		"5 fallos consecutivos no-429 → circuit breaker")
	require.NotNil(t, b.PausedUntil)
	// El breaker disparó en el 5º tick (el reloj avanzó 1s más tras él).
	assert.WithinDuration(t, current.Add(5*time.Minute), *b.PausedUntil, 2*time.Second)

	it0 := reloadItem(t, db, items[0].ID)
	assert.Equal(t, models.RetouchItemStatusFailed, it0.Status, "3 attempts agotados → failed")
	assert.Equal(t, 3, it0.Attempts)
	assert.NotEmpty(t, it0.ErrorMessage)
	assert.False(t, strings.Contains(it0.ErrorMessage, "gemini"),
		"el error crudo no llega al tendero — Art. V")

	it1 := reloadItem(t, db, items[1].ID)
	assert.Equal(t, models.RetouchItemStatusQueued, it1.Status)
	assert.Equal(t, 2, it1.Attempts)
}

// Un éxito intermedio resetea la racha del breaker.
func TestRetouchWorker_SuccessResetsBreaker(t *testing.T) {
	db := setupRetouchDB(t)
	batch, _, _ := seedBatchWithItems(t, db, retouchTestTenantA, 6)
	n := 0
	enhance := funcEnhancer(func(ctx context.Context, tenantID, productID, sourceURL string) (string, error) {
		n++
		if n == 5 {
			return "https://r2/ok.jpg", nil // el 5º intento funciona
		}
		return "", errors.New("gemini upstream 500")
	})
	w, current := newTestWorker(t, db, enhance)
	for i := 0; i < 6; i++ {
		w.Tick(context.Background())
		*current = current.Add(time.Second)
	}
	b := reloadBatch(t, db, batch.ID)
	assert.Equal(t, models.RetouchBatchStatusRunning, b.Status,
		"un éxito corta la racha: el breaker no dispara")
}

// El lote en paused_error se reanuda SOLO al vencer la pausa (AC-10: sin
// intervención del tendero).
func TestRetouchWorker_PausedErrorBatchResumesAfterPause(t *testing.T) {
	db := setupRetouchDB(t)
	batch, items, _ := seedBatchWithItems(t, db, retouchTestTenantA, 1)
	w, current := newTestWorker(t, db, okEnhancer())
	past := current.Add(-time.Second)
	require.NoError(t, db.Model(&models.RetouchBatch{}).Where("id = ?", batch.ID).
		Updates(map[string]any{"status": models.RetouchBatchStatusPausedError,
			"paused_until": past}).Error)

	require.True(t, w.Tick(context.Background()))
	assert.Equal(t, models.RetouchItemStatusReadyForReview,
		reloadItem(t, db, items[0].ID).Status)
}

// Un lote cancelado no se sirve más.
func TestRetouchWorker_CanceledBatchIsNotServed(t *testing.T) {
	db := setupRetouchDB(t)
	batch, _, _ := seedBatchWithItems(t, db, retouchTestTenantA, 1)
	require.NoError(t, db.Model(&models.RetouchBatch{}).Where("id = ?", batch.ID).
		Update("status", models.RetouchBatchStatusCanceled).Error)
	w, _ := newTestWorker(t, db, okEnhancer())
	assert.False(t, w.Tick(context.Background()))
}

// HIGH review: un panic dentro del enhancer (o de cualquier parte de
// processItem) JAMÁS tumba el proceso Go — es el POS de todos los tenants.
// Mismo contrato que runAIJob ("never panics the process"): el ítem queda
// failed con el mensaje en español y el worker sigue vivo al próximo tick.
func TestRetouchWorker_RecoversEnhancerPanic(t *testing.T) {
	db := setupRetouchDB(t)
	_, items, _ := seedBatchWithItems(t, db, retouchTestTenantA, 2)

	calls := 0
	enhance := funcEnhancer(func(ctx context.Context, tenantID, productID, sourceURL string) (string, error) {
		calls++
		if calls == 1 {
			panic("boom en el enhancer")
		}
		return "https://r2/ok-enhanced.jpg", nil
	})
	w, current := newTestWorker(t, db, enhance)

	assert.NotPanics(t, func() { w.Tick(context.Background()) },
		"un panic del enhancer no puede escapar del tick")
	*current = current.Add(time.Second)

	it0 := reloadItem(t, db, items[0].ID)
	assert.Equal(t, models.RetouchItemStatusFailed, it0.Status,
		"el ítem del panic queda failed, no colgado en processing")
	assert.Equal(t, retouchFailedMessage, it0.ErrorMessage,
		"mensaje en español, sin el panic crudo — Art. V")

	// El worker sigue vivo: el siguiente tick procesa el otro ítem.
	require.True(t, w.Tick(context.Background()))
	assert.Equal(t, models.RetouchItemStatusReadyForReview,
		reloadItem(t, db, items[1].ID).Status)
}

// Backstop del cron: recupera huérfanos y procesa hasta maxItems.
func TestRetouchWorker_Backstop(t *testing.T) {
	db := setupRetouchDB(t)
	_, items, _ := seedBatchWithItems(t, db, retouchTestTenantA, 5)
	w, current := newTestWorker(t, db, okEnhancer())

	// Un huérfano viejo que el backstop debe devolver a la cola.
	old := current.Add(-11 * time.Minute)
	require.NoError(t, db.Model(&models.RetouchItem{}).Where("id = ?", items[0].ID).
		Updates(map[string]any{"status": models.RetouchItemStatusProcessing, "started_at": old}).Error)

	recovered, processed := w.Backstop(context.Background(), 3)
	assert.Equal(t, 1, recovered)
	assert.Equal(t, 3, processed)

	var ready int64
	db.Model(&models.RetouchItem{}).
		Where("status = ?", models.RetouchItemStatusReadyForReview).Count(&ready)
	assert.EqualValues(t, 3, ready)
}

// Auditoría 2026-07-10 — BUG: un ítem que vuelve a queued dentro de un lote
// ya NO activo (cancelado) queda atrapado PARA SIEMPRE: pickBatch solo sirve
// lotes running, así que nadie lo procesará jamás, y su estado activo
// (queued) bloquea al producto vía idx_retouch_items_active_product — el
// producto no puede re-encolarse nunca ("ya_en_lote") y deja de contar en
// eligible_count. Escenario real: el worker reclama el ítem (processing), el
// tendero cancela el lote (cancel no toca processing — FR-15) y luego el
// proceso se reinicia (deploy de Render) o el enhance falla transitorio →
// el ítem vuelve a queued dentro del lote cancelado. RecoverStale debe
// LIBERAR esos ítems (canceled) para que el producto vuelva a ser elegible.
func TestRetouchWorker_RecoverStaleReleasesQueuedItemsOfInactiveBatches(t *testing.T) {
	db := setupRetouchDB(t)
	batch, items, products := seedBatchWithItems(t, db, retouchTestTenantA, 2)
	w, current := newTestWorker(t, db, okEnhancer())

	// items[0]: quedó processing y el proceso murió a mitad de foto.
	old := current.Add(-11 * time.Minute)
	require.NoError(t, db.Model(&models.RetouchItem{}).Where("id = ?", items[0].ID).
		Updates(map[string]any{
			"status":     models.RetouchItemStatusProcessing,
			"started_at": old,
		}).Error)
	// El tendero canceló el lote mientras tanto: los queued pasaron a
	// canceled (items[1] simulado abajo re-encolado por un 429 posterior)
	// y el processing quedó intacto (FR-15).
	require.NoError(t, db.Model(&models.RetouchBatch{}).Where("id = ?", batch.ID).
		Update("status", models.RetouchBatchStatusCanceled).Error)

	_, err := w.RecoverStale()
	require.NoError(t, err)

	// Ninguno queda en estado ACTIVO dentro del lote muerto.
	assert.Equal(t, models.RetouchItemStatusCanceled,
		reloadItem(t, db, items[0].ID).Status,
		"huérfano de lote cancelado NO puede volver a queued eterno")
	assert.Equal(t, models.RetouchItemStatusCanceled,
		reloadItem(t, db, items[1].ID).Status,
		"queued atrapado en lote cancelado se libera")

	// Y el producto vuelve a poder encolarse en un lote nuevo: el índice
	// único de activos ya no lo bloquea.
	newBatch := models.RetouchBatch{TenantID: retouchTestTenantA,
		Status: models.RetouchBatchStatusRunning}
	require.NoError(t, db.Create(&newBatch).Error)
	require.NoError(t, db.Create(&models.RetouchItem{
		BatchID:        newBatch.ID,
		TenantID:       retouchTestTenantA,
		ProductID:      products[0].ID,
		SourcePhotoURL: products[0].PhotoURL,
		Status:         models.RetouchItemStatusQueued,
	}).Error, "el producto liberado debe poder re-encolarse")
}

// Auditoría 2026-07-10 — BUG: si el breaker dispara en el ÚLTIMO fallo
// terminal del lote (attempts agotados y sin más queued), el lote queda
// paused_error; al vencer la pausa el Tick lo devuelve a running, pero
// pickBatch ya nunca lo elige (sin queued) y finishBatchIfDrained jamás
// vuelve a correr → lote zombi "running" para siempre (el summary muestra un
// lote activo con 0 trabajo eternamente). Un lote drenado debe quedar
// completed aunque el breaker haya disparado en su último ítem.
func TestRetouchWorker_BreakerOnLastItemStillCompletesBatch(t *testing.T) {
	db := setupRetouchDB(t)
	batch, items, _ := seedBatchWithItems(t, db, retouchTestTenantA, 1)

	// El ítem va por su último intento: el próximo fallo es terminal.
	require.NoError(t, db.Model(&models.RetouchItem{}).Where("id = ?", items[0].ID).
		Update("attempts", retouchMaxAttempts-1).Error)

	failing := funcEnhancer(func(ctx context.Context, tenantID, productID, sourceURL string) (string, error) {
		return "", errors.New("proveedor caído")
	})
	w, _ := newTestWorker(t, db, failing)
	// Racha al borde del umbral: este fallo dispara el breaker.
	w.consecFails[batch.ID] = retouchBreakerThreshold - 1

	require.True(t, w.Tick(context.Background()))

	assert.Equal(t, models.RetouchItemStatusFailed,
		reloadItem(t, db, items[0].ID).Status)
	assert.Equal(t, models.RetouchBatchStatusCompleted,
		reloadBatch(t, db, batch.ID).Status,
		"lote drenado queda completed, nunca zombi paused_error/running")
}
