// Spec: specs/098-aporte-automatico-catalogo/spec.md — Fase 2.
package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"vendia-backend/internal/models"
)

// setupAutoContributeDB extiende el esquema de catálogo con una tabla `tenants`
// mínima (id + terms_accepted_version + deleted_at) para poder probar el gate de
// aceptación de términos de AutoContributeProductPhoto sin un Postgres real.
func setupAutoContributeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupCatalogServiceDB(t)
	require.NoError(t, db.Exec(`
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY,
			terms_accepted_version TEXT,
			deleted_at DATETIME
		);
	`).Error)
	return db
}

func seedTenant(t *testing.T, db *gorm.DB, id, termsVersion string) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, terms_accepted_version) VALUES (?, ?)`,
		id, termsVersion,
	).Error)
}

func catalogStatus(db *gorm.DB, barcode string) string {
	var status string
	db.Table("catalog_products").Where("barcode = ?", barcode).Pluck("status", &status)
	return status
}

func countCatalogRows(db *gorm.DB, barcode string) int64 {
	var n int64
	db.Table("catalog_products").Where("barcode = ?", barcode).Count(&n)
	return n
}

// validProduct — producto apto (barcode retail válido + todos los campos).
func validProduct(tenantID string) models.Product {
	p := models.Product{
		TenantID:     tenantID,
		Name:         "Coca-Cola 400ml",
		Barcode:      "7702005004467", // EAN-13 con checksum válido
		Presentation: "botella",
		Content:      "400ml",
		Category:     "bebidas",
		PhotoURL:     "https://r2.vendia.store/tenant-a/coca.jpg",
	}
	return p
}

// TestAutoContribute_NoAporta_BarcodeInvalido: un barcode que no es de retail
// (SKU interno) nunca califica para el catálogo compartido.
func TestAutoContribute_NoAporta_BarcodeInvalido(t *testing.T) {
	db := setupAutoContributeDB(t)
	svc := NewCatalogService(db, nil)
	seedTenant(t, db, "tenant-a", models.CatalogTermsVersion)

	p := validProduct("tenant-a")
	p.Barcode = "VND-123" // SKU interno → inválido

	svc.AutoContributeProductPhoto(context.Background(), nil, "tenant-a", p)

	assert.Equal(t, int64(0), countCatalogRows(db, "VND-123"),
		"un SKU interno no debe crear producto de catálogo")
}

// TestAutoContribute_NoAporta_FaltaContent: sin descripción (content) el
// producto no está lo bastante completo para el catálogo compartido.
func TestAutoContribute_NoAporta_FaltaContent(t *testing.T) {
	db := setupAutoContributeDB(t)
	svc := NewCatalogService(db, nil)
	seedTenant(t, db, "tenant-a", models.CatalogTermsVersion)

	p := validProduct("tenant-a")
	p.Content = "" // falta descripción/medida

	svc.AutoContributeProductPhoto(context.Background(), nil, "tenant-a", p)

	assert.Equal(t, int64(0), countCatalogRows(db, p.Barcode),
		"sin content no se aporta")
}

// TestAutoContribute_NoAporta_FaltaPresentacion.
func TestAutoContribute_NoAporta_FaltaPresentacion(t *testing.T) {
	db := setupAutoContributeDB(t)
	svc := NewCatalogService(db, nil)
	seedTenant(t, db, "tenant-a", models.CatalogTermsVersion)

	p := validProduct("tenant-a")
	p.Presentation = ""

	svc.AutoContributeProductPhoto(context.Background(), nil, "tenant-a", p)

	assert.Equal(t, int64(0), countCatalogRows(db, p.Barcode),
		"sin presentación no se aporta")
}

// TestAutoContribute_NoAporta_SinFoto.
func TestAutoContribute_NoAporta_SinFoto(t *testing.T) {
	db := setupAutoContributeDB(t)
	svc := NewCatalogService(db, nil)
	seedTenant(t, db, "tenant-a", models.CatalogTermsVersion)

	p := validProduct("tenant-a")
	p.PhotoURL = ""
	p.ImageURL = ""

	svc.AutoContributeProductPhoto(context.Background(), nil, "tenant-a", p)

	assert.Equal(t, int64(0), countCatalogRows(db, p.Barcode),
		"sin foto no se aporta")
}

// TestAutoContribute_NoAporta_TenantNoAceptoTerminos: el tenant no aceptó la
// versión vigente de los términos → nunca se aporta, aun con todo lo demás OK.
func TestAutoContribute_NoAporta_TenantNoAceptoTerminos(t *testing.T) {
	db := setupAutoContributeDB(t)
	svc := NewCatalogService(db, nil)
	seedTenant(t, db, "tenant-a", "2000-01-01") // versión vieja

	p := validProduct("tenant-a")

	// gemini nil ⇒ VerifyImageMatchesProduct devolvería false igualmente, pero
	// el gate de términos corta ANTES de siquiera intentar la verificación.
	svc.AutoContributeProductPhoto(context.Background(), nil, "tenant-a", p)

	assert.Equal(t, int64(0), countCatalogRows(db, p.Barcode),
		"sin términos vigentes aceptados no se aporta")
}

// TestAutoContribute_NoAporta_TenantInexistente: si no hay fila de tenant, el
// gate de términos falla y no se aporta.
func TestAutoContribute_NoAporta_TenantInexistente(t *testing.T) {
	db := setupAutoContributeDB(t)
	svc := NewCatalogService(db, nil)

	p := validProduct("fantasma")

	svc.AutoContributeProductPhoto(context.Background(), nil, "fantasma", p)

	assert.Equal(t, int64(0), countCatalogRows(db, p.Barcode),
		"un tenant inexistente no puede aportar")
}

// TestAutoContribute_NoAporta_IANoConfirma: con todos los gates deterministas
// OK y términos aceptados, si la IA no confirma (gemini nil ⇒ VerifyImage
// devuelve false) NO se aporta. Cubre la rama de verificación IA de forma
// determinista sin llamar a la red.
func TestAutoContribute_NoAporta_IANoConfirma(t *testing.T) {
	db := setupAutoContributeDB(t)
	svc := NewCatalogService(db, nil)
	seedTenant(t, db, "tenant-a", models.CatalogTermsVersion)

	p := validProduct("tenant-a")

	// gemini == nil → VerifyImageMatchesProduct(s==nil) devuelve (false, nil)
	// → no confirma → no aporta.
	svc.AutoContributeProductPhoto(context.Background(), nil, "tenant-a", p)

	assert.Equal(t, int64(0), countCatalogRows(db, p.Barcode),
		"si la IA no confirma la imagen, no se aporta")
	assert.Equal(t, "", catalogStatus(db, p.Barcode))
}

// ── Decisión del fundador 2026-07-14 (recomendación del concilio, B03) ──────
// El aporte AUTOMÁTICO usa el MISMO consenso que el manual: verified solo con
// 2 tenants DISTINTOS. Un tenant solo (aunque la IA confirme) deja el
// producto pending; el mismo tenant aportando dos veces nunca cuenta doble.

func TestAutoContribute_UnSoloTenant_QuedaPending(t *testing.T) {
	db := setupAutoContributeDB(t)
	svc := NewCatalogService(db, nil)

	p := validProduct("tenant-a")
	svc.contributeVerifiedPhoto("tenant-a", p, p.PhotoURL)

	require.Equal(t, int64(1), countCatalogRows(db, p.Barcode))
	assert.Equal(t, "pending", catalogStatus(db, p.Barcode),
		"1 solo tenant (aun con IA confirmando) no basta para verified")

	// Re-aporte del MISMO tenant: sigue pending (no cuenta doble).
	svc.contributeVerifiedPhoto("tenant-a", p, p.PhotoURL)
	assert.Equal(t, "pending", catalogStatus(db, p.Barcode))
}

func TestAutoContribute_SegundoTenantDistinto_Verifica(t *testing.T) {
	db := setupAutoContributeDB(t)
	svc := NewCatalogService(db, nil)

	pA := validProduct("tenant-a")
	svc.contributeVerifiedPhoto("tenant-a", pA, pA.PhotoURL)
	require.Equal(t, "pending", catalogStatus(db, pA.Barcode))

	pB := validProduct("tenant-b")
	pB.PhotoURL = "https://r2.vendia.store/tenant-b/coca.jpg"
	svc.contributeVerifiedPhoto("tenant-b", pB, pB.PhotoURL)

	assert.Equal(t, "verified", catalogStatus(db, pA.Barcode),
		"2 tenants distintos = consenso → verified (igual que la vía manual)")
}
