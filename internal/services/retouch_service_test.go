// Spec: specs/101-retocar-fotos-inventario/spec.md
package services

import (
	"testing"

	"vendia-backend/internal/database"
	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	retouchTestTenantA = "11111111-1111-1111-1111-111111111111"
	retouchTestTenantB = "22222222-2222-2222-2222-222222222222"
)

// setupRetouchDB abre una SQLite in-memory con el esquema mínimo del flujo de
// retoque (products + retouch_batches + retouch_items) y el índice UNIQUE
// parcial que en producción instala el bootstrap (database.ApplyRetouchIndexes
// — SQL portable a ambos dialectos, por eso el test lo puede aplicar tal cual).
func setupRetouchDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Product{}, &models.RetouchBatch{}, &models.RetouchItem{}))
	require.NoError(t, database.ApplyRetouchIndexes(db))
	return db
}

// ── T-01 — elegibilidad server-side (FR-01, FR-02, AC-01) ────────────────

func TestIsProductRetouchEligible_OwnRawPhotoIsEligible(t *testing.T) {
	p := models.Product{
		TenantID: retouchTestTenantA,
		PhotoURL: "https://r2.vendia.co/products/" + retouchTestTenantA + "/abc.jpg",
	}
	assert.True(t, IsProductRetouchEligible(p, retouchTestTenantA))
}

func TestIsProductRetouchEligible_ImageURLFallback(t *testing.T) {
	// PhotoURL vacío pero ImageURL propia cruda → elegible (la foto ACTUAL es
	// la que manda, igual que el flujo /enhance que usa PhotoURL || ImageURL).
	p := models.Product{
		TenantID: retouchTestTenantA,
		ImageURL: "https://r2.vendia.co/products/" + retouchTestTenantA + "/abc.jpg",
	}
	assert.True(t, IsProductRetouchEligible(p, retouchTestTenantA))
}

// Ajuste Spec 101: las fotos EXTERNAS de enriquecimiento por barcode
// (OpenFoodFacts, VTEX…) suelen ser crudas — una mano sosteniendo la botella —
// y deben contar como "sin retocar". Externa = host fuera de nuestro storage.
func TestIsProductRetouchEligible_ExternalEnrichmentPhotoIsEligible(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"OpenFoodFacts (caso Gatorade maracuyá)",
			"https://images.openfoodfacts.org/images/products/775/510/431/1652/front_fr.3.200.jpg"},
		{"VTEX", "https://exitocol.vteximg.com.br/arquivos/ids/1234567/gatorade.jpg"},
		{"host externo genérico", "https://cdn.example.com/fotos/botella.png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := models.Product{TenantID: retouchTestTenantA, ImageURL: tc.url}
			assert.True(t, IsProductRetouchEligible(p, retouchTestTenantA),
				"externa cruda debe ser ELEGIBLE: %s", tc.url)
		})
	}
}

func TestIsProductRetouchEligible_Exclusions(t *testing.T) {
	own := "https://r2.vendia.co/products/" + retouchTestTenantA + "/abc.jpg"
	cases := []struct {
		name string
		p    models.Product
	}{
		{"sin foto", models.Product{TenantID: retouchTestTenantA}},
		{"ya mejorada con IA (flag)", models.Product{
			TenantID: retouchTestTenantA, PhotoURL: own, IsAIEnhanced: true}},
		{"foto muestra IA de plato", models.Product{
			TenantID: retouchTestTenantA, PhotoURL: own, PhotoIsSample: true}},
		{"URL -enhanced (mejorada, flag viejo perdido)", models.Product{
			TenantID: retouchTestTenantA,
			PhotoURL: "https://r2.vendia.co/products/" + retouchTestTenantA + "/abc-enhanced.jpg?v=123"}},
		{"URL -generated (creada con IA)", models.Product{
			TenantID: retouchTestTenantA,
			PhotoURL: "https://r2.vendia.co/products/" + retouchTestTenantA + "/abc-generated.png"}},
		{"URL -enhanced en host EXTERNO (mejorada re-referenciada)", models.Product{
			TenantID: retouchTestTenantA,
			PhotoURL: "https://cdn.example.com/fotos/abc-enhanced.jpg"}},
		{"catálogo compartido: bucket R2 de OTRO tenant (curada)", models.Product{
			TenantID: retouchTestTenantA,
			PhotoURL: "https://r2.vendia.co/products/" + retouchTestTenantB + "/abc.jpg"}},
		{"catálogo compartido: Supabase storage de OTRO tenant (curada)", models.Product{
			TenantID: retouchTestTenantA,
			PhotoURL: "https://xyz.supabase.co/storage/v1/object/public/product-photos/products/" + retouchTestTenantB + "/abc.jpg"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.False(t, IsProductRetouchEligible(tc.p, retouchTestTenantA),
				"debe ser INELEGIBLE: %s", tc.name)
		})
	}
}

// EligibleRetouchProducts recalcula server-side (Art. III): mezcla AC-01 —
// 3 crudas propias + 1 externa (enriquecimiento) cuentan; 1 mejorada, 1 del
// catálogo compartido (storage de otro tenant), 1 muestra, 1 sin foto y 1
// draft no.
func TestEligibleRetouchProducts_CountsRawOwnAndExternalPhotos(t *testing.T) {
	db := setupRetouchDB(t)
	own := func(name string) models.Product {
		return models.Product{
			TenantID: retouchTestTenantA, Name: name, Price: 1000,
			PhotoURL: "https://r2.vendia.co/products/" + retouchTestTenantA + "/" + name + ".jpg",
		}
	}
	raw1, raw2, raw3 := own("p1"), own("p2"), own("p3")
	enhanced := own("p4")
	enhanced.IsAIEnhanced = true
	external := models.Product{TenantID: retouchTestTenantA, Name: "p5", Price: 1,
		ImageURL: "https://images.openfoodfacts.org/images/products/775/510/431/1652/front_fr.3.200.jpg"}
	sample := own("p6")
	sample.PhotoIsSample = true
	noPhoto := models.Product{TenantID: retouchTestTenantA, Name: "p7", Price: 1}
	draft := own("p8")
	draft.IsDraft = true
	otherTenant := models.Product{TenantID: retouchTestTenantB, Name: "p9", Price: 1,
		PhotoURL: "https://r2.vendia.co/products/" + retouchTestTenantB + "/p9.jpg"}
	sharedCatalog := models.Product{TenantID: retouchTestTenantA, Name: "p10", Price: 1,
		PhotoURL: "https://r2.vendia.co/products/" + retouchTestTenantB + "/p10.jpg"}

	for _, p := range []*models.Product{&raw1, &raw2, &raw3, &enhanced, &external, &sample, &noPhoto, &draft, &otherTenant, &sharedCatalog} {
		require.NoError(t, db.Create(p).Error)
	}

	eligible, err := EligibleRetouchProducts(db, retouchTestTenantA)
	require.NoError(t, err)
	assert.Len(t, eligible, 4,
		"AC-01 ajustado: 3 crudas propias + 1 externa de enriquecimiento")
	ids := map[string]bool{}
	for _, p := range eligible {
		ids[p.ID] = true
	}
	assert.True(t, ids[raw1.ID] && ids[raw2.ID] && ids[raw3.ID] && ids[external.ID])
}

// ── T-01 — UNIQUE parcial: un producto no vive en dos lotes activos ──────

func TestRetouchActiveIndex_BlocksSameProductTwiceWhileActive(t *testing.T) {
	db := setupRetouchDB(t)
	productID := "33333333-3333-3333-3333-333333333333"

	// (El caso "dos lotes activos" ya es imposible por el índice de lotes —
	// ver TestRetouchBatchIndex_*; acá el invariante de ÍTEM: el mismo
	// producto no puede tener dos ítems activos, ni siquiera en el mismo
	// lote, p. ej. dos requests que pasaron el pre-check a la vez.)
	b1 := models.RetouchBatch{TenantID: retouchTestTenantA, Status: models.RetouchBatchStatusRunning}
	require.NoError(t, db.Create(&b1).Error)

	first := models.RetouchItem{BatchID: b1.ID, TenantID: retouchTestTenantA,
		ProductID: productID, SourcePhotoURL: "https://r2/x.jpg",
		Status: models.RetouchItemStatusQueued}
	require.NoError(t, db.Create(&first).Error)

	dup := models.RetouchItem{BatchID: b1.ID, TenantID: retouchTestTenantA,
		ProductID: productID, SourcePhotoURL: "https://r2/x.jpg",
		Status: models.RetouchItemStatusQueued}
	err := db.Create(&dup).Error
	require.Error(t, err, "el índice UNIQUE parcial debe impedir el duplicado activo")
	assert.True(t, IsRetouchActiveUniqueViolation(err),
		"la violación debe ser reconocible para mapearla a skipped[]: %v", err)

	// ready_for_review sigue siendo activo → también bloquea.
	require.NoError(t, db.Model(&models.RetouchItem{}).Where("id = ?", first.ID).
		Update("status", models.RetouchItemStatusReadyForReview).Error)
	assert.Error(t, db.Create(&models.RetouchItem{BatchID: b1.ID,
		TenantID: retouchTestTenantA, ProductID: productID,
		Status: models.RetouchItemStatusQueued}).Error)
}

func TestRetouchActiveIndex_AllowsRequeueAfterTerminalState(t *testing.T) {
	db := setupRetouchDB(t)
	productID := "44444444-4444-4444-4444-444444444444"

	b1 := models.RetouchBatch{TenantID: retouchTestTenantA, Status: models.RetouchBatchStatusCompleted}
	require.NoError(t, db.Create(&b1).Error)
	done := models.RetouchItem{BatchID: b1.ID, TenantID: retouchTestTenantA,
		ProductID: productID, Status: models.RetouchItemStatusConfirmed}
	require.NoError(t, db.Create(&done).Error)

	// Confirmado (terminal) NO bloquea re-encolar en un lote nuevo (FR-13:
	// la idempotencia es sobre lotes ACTIVOS; una foto nueva puede re-entrar).
	b2 := models.RetouchBatch{TenantID: retouchTestTenantA, Status: models.RetouchBatchStatusRunning}
	require.NoError(t, db.Create(&b2).Error)
	again := models.RetouchItem{BatchID: b2.ID, TenantID: retouchTestTenantA,
		ProductID: productID, Status: models.RetouchItemStatusQueued}
	assert.NoError(t, db.Create(&again).Error)
}

// MEDIUM 1 review: máx 1 lote ACTIVO por tenant hecho físico — la carrera de
// dos POST simultáneos (doble-tap, dueño+empleado) no puede dejar dos lotes
// running (el summary solo muestra uno → el progreso del otro sería
// invisible, rompiendo AC-09).
func TestRetouchBatchIndex_BlocksTwoActiveBatchesPerTenant(t *testing.T) {
	db := setupRetouchDB(t)
	b1 := models.RetouchBatch{TenantID: retouchTestTenantA, Status: models.RetouchBatchStatusRunning}
	require.NoError(t, db.Create(&b1).Error)

	dupRunning := models.RetouchBatch{TenantID: retouchTestTenantA, Status: models.RetouchBatchStatusRunning}
	err := db.Create(&dupRunning).Error
	require.Error(t, err, "dos lotes running del mismo tenant deben chocar")
	assert.True(t, IsRetouchBatchActiveUniqueViolation(err),
		"la violación debe ser reconocible para re-seleccionar el ganador: %v", err)

	// paused_error también es activo (se reanuda solo) → también bloquea.
	dupPaused := models.RetouchBatch{TenantID: retouchTestTenantA, Status: models.RetouchBatchStatusPausedError}
	assert.Error(t, db.Create(&dupPaused).Error)
}

func TestRetouchBatchIndex_TerminalBatchesDoNotBlockNewOne(t *testing.T) {
	db := setupRetouchDB(t)
	done := models.RetouchBatch{TenantID: retouchTestTenantA, Status: models.RetouchBatchStatusCompleted}
	canceled := models.RetouchBatch{TenantID: retouchTestTenantA, Status: models.RetouchBatchStatusCanceled}
	require.NoError(t, db.Create(&done).Error)
	require.NoError(t, db.Create(&canceled).Error)

	fresh := models.RetouchBatch{TenantID: retouchTestTenantA, Status: models.RetouchBatchStatusRunning}
	assert.NoError(t, db.Create(&fresh).Error,
		"completed/canceled no bloquean un lote nuevo")
}

func TestRetouchBatchIndex_DifferentTenantsDoNotCollide(t *testing.T) {
	db := setupRetouchDB(t)
	require.NoError(t, db.Create(&models.RetouchBatch{
		TenantID: retouchTestTenantA, Status: models.RetouchBatchStatusRunning}).Error)
	assert.NoError(t, db.Create(&models.RetouchBatch{
		TenantID: retouchTestTenantB, Status: models.RetouchBatchStatusRunning}).Error)
}

func TestRetouchActiveIndex_DifferentTenantsSameProductIDDoNotCollide(t *testing.T) {
	// Art. III: el índice es por (tenant_id, product_id) — dos tenants con el
	// mismo product_id (imposible en la práctica, defensivo) no chocan.
	db := setupRetouchDB(t)
	productID := "55555555-5555-5555-5555-555555555555"
	bA := models.RetouchBatch{TenantID: retouchTestTenantA, Status: models.RetouchBatchStatusRunning}
	bB := models.RetouchBatch{TenantID: retouchTestTenantB, Status: models.RetouchBatchStatusRunning}
	require.NoError(t, db.Create(&bA).Error)
	require.NoError(t, db.Create(&bB).Error)
	require.NoError(t, db.Create(&models.RetouchItem{BatchID: bA.ID,
		TenantID: retouchTestTenantA, ProductID: productID,
		Status: models.RetouchItemStatusQueued}).Error)
	assert.NoError(t, db.Create(&models.RetouchItem{BatchID: bB.ID,
		TenantID: retouchTestTenantB, ProductID: productID,
		Status: models.RetouchItemStatusQueued}).Error)
}
