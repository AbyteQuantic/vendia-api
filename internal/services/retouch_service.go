// Spec: specs/101-retocar-fotos-inventario/spec.md
//
// Elegibilidad server-side del retoque masivo (FR-01/FR-02) + helpers de
// idempotencia. Vive en services (patrón Spec 100, product_barcode.go) porque
// lo usan el handler de lotes, el summary y el worker — handlers importa
// services, nunca al revés.
package services

import (
	"strings"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// RetouchActiveUniqueIndex es el índice único parcial (tenant_id, product_id)
// sobre estados activos, creado en el bootstrap (database.ApplyRetouchIndexes).
// El nombre se usa para reconocer su violación en Postgres.
const RetouchActiveUniqueIndex = "idx_retouch_items_active_product"

// RetouchSourcePhotoURL devuelve la foto ACTUAL efectiva del producto — la
// misma regla que el flujo /enhance individual (PhotoURL con fallback a
// ImageURL). El estado "sin retocar" siempre es sobre la foto ACTUAL (spec §9).
func RetouchSourcePhotoURL(p models.Product) string {
	if p.PhotoURL != "" {
		return p.PhotoURL
	}
	return p.ImageURL
}

// IsProductRetouchEligible decide si la foto actual del producto es una
// original propia sin retocar (FR-01). Elegible = foto propia del bucket del
// tenant (URL contiene products/<tenantID>/ sin sufijo -enhanced/-generated),
// !is_ai_enhanced, !photo_is_sample y foto no vacía. Las fotos del catálogo
// compartido / externas no contienen el prefijo del tenant → excluidas.
func IsProductRetouchEligible(p models.Product, tenantID string) bool {
	if tenantID == "" || p.IsAIEnhanced || p.PhotoIsSample {
		return false
	}
	photo := RetouchSourcePhotoURL(p)
	if photo == "" {
		return false
	}
	if !strings.Contains(photo, "products/"+tenantID+"/") {
		return false
	}
	if strings.Contains(photo, "-enhanced.") || strings.Contains(photo, "-generated.") {
		return false
	}
	return true
}

// EligibleRetouchProducts recalcula server-side (Art. III) las referencias
// del tenant cuya foto sigue cruda. El pre-filtro SQL descarta lo barato
// (flags, drafts) y el patrón de URL se decide en Go con el MISMO predicado
// IsProductRetouchEligible que usa el worker — una sola fuente de verdad.
// Los drafts (IsDraft) se excluyen igual que en ListProducts: un producto
// que el tendero nunca guardó no es inventario real (Art. VII).
func EligibleRetouchProducts(db *gorm.DB, tenantID string) ([]models.Product, error) {
	var candidates []models.Product
	if err := db.Where(
		"tenant_id = ? AND is_ai_enhanced = ? AND photo_is_sample = ? AND is_draft = ?",
		tenantID, false, false, false).
		Where("(photo_url IS NOT NULL AND photo_url != '') OR (image_url IS NOT NULL AND image_url != '')").
		Find(&candidates).Error; err != nil {
		return nil, err
	}
	eligible := make([]models.Product, 0, len(candidates))
	for _, p := range candidates {
		if IsProductRetouchEligible(p, tenantID) {
			eligible = append(eligible, p)
		}
	}
	return eligible, nil
}

// IsRetouchActiveUniqueViolation detecta la violación del índice único
// parcial de ítems activos — la carrera/re-encolado que el handler mapea a
// skipped[] (FR-13). Postgres incluye el nombre del índice en el error;
// SQLite (tests) reporta las columnas de la tabla. Mismo criterio
// string-match de IsProductBarcodeUniqueViolation (Spec 100).
func IsRetouchActiveUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, RetouchActiveUniqueIndex) ||
		strings.Contains(msg, "UNIQUE constraint failed: retouch_items")
}

// IsRateLimitMessage reconoce un error de cuota/límite del proveedor de IA
// (429). Fuente única de patrones: la comparte el clasificador de jobs de
// foto (handlers/ai_job_error_classify.go) y el backoff del worker de
// retoque (AC-10). El texto se normaliza a minúsculas acá.
func IsRateLimitMessage(raw string) bool {
	raw = strings.ToLower(raw)
	return strings.Contains(raw, "returned 429") ||
		strings.Contains(raw, "error 429") ||
		strings.Contains(raw, "rate limit") ||
		strings.Contains(raw, "too many requests") ||
		strings.Contains(raw, "resource_exhausted")
}

// IsRateLimitError es IsRateLimitMessage sobre un error no-nil.
func IsRateLimitError(err error) bool {
	return err != nil && IsRateLimitMessage(err.Error())
}
