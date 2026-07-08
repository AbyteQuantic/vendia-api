// Spec: specs/101-retocar-fotos-inventario/spec.md
//
// runFaithfulEnhance extraído del worker de /enhance (Spec 094): el camino
// FIEL no-generativo — Gemini solo aporta la MÁSCARA (SegmentProductMask) y
// el ángulo (EstimateUprightRotation); el resultado son los PÍXELES REALES
// compuestos en Go (ComposeFaithful) y subidos a R2 con la clave determinista
// products/<t>/<uuid>-enhanced.jpg + cache-bust ?v=. Lo comparten:
//   - el flujo /enhance individual (handlers/inventory.go, default sin mode),
//     que después SÍ aplica al producto + autoContribute (contrato intacto);
//   - el worker del lote (retouch_worker.go), que guarda el resultado en
//     retouch_items.candidate_url SIN tocar products (FR-05).
//
// El lote JAMÁS usa mode=improve/studio (generativos): esta función es su
// única entrada y no existe parámetro mode — riesgo #1 del plan cerrado por
// construcción.
package services

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// ProductSegmenter es lo ÚNICO que el camino fiel necesita de Gemini: la
// máscara del producto y el ángulo para enderezarlo. *GeminiService lo
// satisface. Interface pequeña definida donde se usa — y garantía de que el
// lote no puede invocar los métodos generativos.
type ProductSegmenter interface {
	SegmentProductMask(ctx context.Context, imageData []byte, mimeType string) ([]byte, error)
	EstimateUprightRotation(ctx context.Context, imageData []byte, mimeType string) (float64, error)
}

// FaithfulEnhanceDeps son las dependencias inyectables del camino fiel.
type FaithfulEnhanceDeps struct {
	Gemini   ProductSegmenter
	Storage  FileStorage
	Download func(ctx context.Context, url string) ([]byte, string, error)
	Now      func() time.Time
}

// RunFaithfulEnhance ejecuta el camino FIEL completo para una foto:
// descarga → máscara (fail-safe) → rotación (fail-safe 0) → ComposeFaithful
// → upload R2 + ?v=. Devuelve la URL nueva SIN tocar el producto — aplicar
// (o no) es decisión del caller. Los wraps de error conservan los textos que
// classifyAIJobError reconoce (download_failed / upload_failed).
func RunFaithfulEnhance(ctx context.Context, deps FaithfulEnhanceDeps, tenantID, productID, sourceURL string) (string, error) {
	download := deps.Download
	if download == nil {
		download = DownloadImage
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}

	imageData, contentType, err := download(ctx, sourceURL)
	if err != nil {
		return "", err
	}

	// Máscara fail-safe (Spec 094): si Gemini no la da, el composite cae a
	// solo-realce de la foto original — nunca un resultado peor.
	mask, mErr := deps.Gemini.SegmentProductMask(ctx, imageData, contentType)
	if mErr != nil {
		mask = nil
	}

	// Rotación fail-safe: cualquier error → 0° (no rotar). Mismo log
	// grep-able que tenía handlers.uprightRotation.
	deg, rErr := deps.Gemini.EstimateUprightRotation(ctx, imageData, contentType)
	if rErr != nil {
		log.Printf("[UPRIGHT-ROTATION] error, no se rota (fail-safe 0): %v", rErr)
		deg = 0
	} else {
		log.Printf("[UPRIGHT-ROTATION] ángulo estimado: %.1f°", deg)
	}

	enhanced, err := ComposeFaithful(imageData, mask, deg)
	if err != nil {
		return "", fmt.Errorf("error al mejorar foto: %w", err)
	}

	key := fmt.Sprintf("products/%s/%s-enhanced.jpg", tenantID, productID)
	newURL, err := deps.Storage.Upload(ctx, "product-photos", key, enhanced, "image/jpeg")
	if err != nil {
		return "", fmt.Errorf("error al guardar foto mejorada: %w", err)
	}
	// Cache-bust: la clave es determinista; sin ?v el cliente mostraría la
	// versión vieja en caché al re-mejorar el mismo producto (Spec 094).
	return fmt.Sprintf("%s?v=%d", newURL, now().UnixNano()), nil
}

// FaithfulRetouchEnhancer empaqueta RunFaithfulEnhance con sus dependencias
// de producción para el worker del lote. Download es un campo para que los
// tests lo intercepten sin red.
type FaithfulRetouchEnhancer struct {
	Gemini   ProductSegmenter
	Storage  FileStorage
	Download func(ctx context.Context, url string) ([]byte, string, error)
}

// NewFaithfulRetouchEnhancer construye el enhancer del lote. gemini/storage
// nil → el caller (main) simplemente no arranca el worker.
func NewFaithfulRetouchEnhancer(gemini ProductSegmenter, storage FileStorage) *FaithfulRetouchEnhancer {
	return &FaithfulRetouchEnhancer{Gemini: gemini, Storage: storage, Download: DownloadImage}
}

// Run procesa una foto del lote por el camino fiel y devuelve la candidate
// URL. No toca products: eso solo pasa en el confirm del tendero.
func (e *FaithfulRetouchEnhancer) Run(ctx context.Context, tenantID, productID, sourceURL string) (string, error) {
	return RunFaithfulEnhance(ctx, FaithfulEnhanceDeps{
		Gemini:   e.Gemini,
		Storage:  e.Storage,
		Download: e.Download,
	}, tenantID, productID, sourceURL)
}

// DownloadImage descarga la foto fuente para el camino fiel. Es el MISMO
// código (y los MISMOS wraps en español, que classifyAIJobError reconoce)
// que vivía en handlers.downloadSourceImage — movido acá porque el worker
// del lote también lo necesita y handlers importa services, nunca al revés.
// handlers.downloadSourceImage ahora delega en esta función.
func DownloadImage(ctx context.Context, sourceURL string) ([]byte, string, error) {
	imgReq, err := http.NewRequestWithContext(ctx, "GET", sourceURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("error al preparar descarga de foto: %w", err)
	}
	imgReq.Header.Set("User-Agent", "VendIA-POS/1.0 (vendia.co)")

	resp, err := http.DefaultClient.Do(imgReq)
	if err != nil {
		return nil, "", fmt.Errorf("error al obtener foto: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("la URL de la foto devolvió %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		return nil, "", fmt.Errorf("la URL no contiene una imagen válida")
	}

	imageData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("error al leer foto: %w", err)
	}
	return imageData, contentType, nil
}
