package handlers

import (
	"context"
	"io"
	"net/http"
	"time"

	"vendia-backend/internal/aiusage"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Max audio size accepted for voice-to-catalog. Gemini's multimodal
// endpoint caps total request payload around 20 MiB; 10 MiB leaves
// headroom for base64 expansion (raw * 4/3) plus prompt overhead.
// In Flutter terms a press-and-hold recording cap of ~60s at 128 kbps
// m4a comfortably fits under this ceiling.
const maxVoiceAudioBytes = 10 << 20 // 10 MiB

// VoiceInventoryProductResult mirrors ScanInvoice's ProductResult shape
// (internal/handlers/inventory.go) so the shared review UI
// (IaResultScreen) consumes voice-sourced and invoice-sourced items
// identically — same fields, same coincidence-match contract.
//
// Spec: specs/099-inventario-voz-factura-campos-separados/spec.md — FR-01, FR-03.
type VoiceInventoryProductResult struct {
	Name           string  `json:"name"`
	Presentation   string  `json:"presentation,omitempty"`
	Content        string  `json:"content,omitempty"`
	Quantity       int     `json:"quantity"`
	PurchasePrice  float64 `json:"purchase_price"`
	SellPrice      float64 `json:"sell_price"`
	Barcode        string  `json:"barcode,omitempty"`
	Status         string  `json:"status"`
	MatchProductID string  `json:"match_product_id,omitempty"`
	MatchMethod    string  `json:"match_method,omitempty"`
}

// voiceInventoryResponse wraps the parsed array in the standard
// envelope so the Flutter client can read `data[]` the same way it
// handles every other admin response.
type voiceInventoryResponse struct {
	Data []VoiceInventoryProductResult `json:"data"`
}

// buildVoiceInventoryResults runs the same coincidence-matching
// pipeline ScanInvoice uses (Spec 099 FR-05 — one shared service,
// branch-scoped) against each already-parsed item. Kept as a pure
// function (no Gemini call inside) so it's directly unit-testable with
// a hand-built []VoiceInventoryItem and an in-memory DB — the handler
// below is just the HTTP plumbing around it.
func buildVoiceInventoryResults(db *gorm.DB, tenantID, branchID string, items []services.VoiceInventoryItem) []VoiceInventoryProductResult {
	reqs := make([]services.MatchProductRequest, len(items))
	for i, it := range items {
		reqs[i] = services.MatchProductRequest{
			Name:         it.Name,
			Barcode:      it.Barcode,
			Presentation: it.Presentation,
			Content:      it.Content,
		}
	}
	matches := services.MatchProducts(db, tenantID, reqs, branchID)

	results := make([]VoiceInventoryProductResult, len(items))
	for i, it := range items {
		status, matchID, matchMethod := services.BestMatchStatus(matches[i])
		results[i] = VoiceInventoryProductResult{
			Name:           it.Name,
			Presentation:   it.Presentation,
			Content:        it.Content,
			Quantity:       it.Quantity,
			PurchasePrice:  it.PurchasePrice,
			SellPrice:      it.SellPrice,
			Barcode:        it.Barcode,
			Status:         status,
			MatchProductID: matchID,
			MatchMethod:    matchMethod,
		}
	}
	return results
}

// VoiceInventory is mounted under /api/v1/ai/voice-inventory and
// gated by PremiumAuth (subscription TRIAL or PRO_ACTIVE only).
// Accepts multipart/form-data with an `audio_file` field. On success
// returns `{"data": [{name, presentation, content, quantity,
// purchase_price, sell_price, barcode, status, match_product_id,
// match_method}]}`.
func VoiceInventory(db *gorm.DB, geminiSvc *services.GeminiService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "servicio de IA no configurado",
			})
			return
		}

		file, header, err := c.Request.FormFile("audio_file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "archivo de audio requerido (campo: audio_file)",
			})
			return
		}
		defer file.Close()

		if header.Size <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "archivo vacío — grabe al menos un segundo",
			})
			return
		}
		if header.Size > maxVoiceAudioBytes {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "el audio supera el máximo de 10MB",
			})
			return
		}

		mimeType := header.Header.Get("Content-Type")
		if !services.IsSupportedAudioMimeType(mimeType) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "formato de audio no soportado",
				"error_code": "unsupported_audio_type",
				"supported":  []string{"audio/mp3", "audio/m4a", "audio/wav", "audio/aac", "audio/ogg", "audio/webm"},
				"received":   mimeType,
			})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo leer el audio",
			})
			return
		}

		tenantID := middleware.GetTenantID(c)

		// 45s covers Gemini's typical multimodal latency for ~60s
		// audio clips with headroom for re-tries inside the SDK.
		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), tenantID),
			45*time.Second,
		)
		defer cancel()

		items, err := geminiSvc.ExtractVoiceInventory(ctx, data, mimeType)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":  "no se pudo interpretar el audio. Intente grabar de nuevo, más cerca del micrófono.",
				"detail": err.Error(),
			})
			return
		}
		if len(items) == 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "no identificamos productos en el audio. Intente mencionar nombre, cantidad y precio.",
			})
			return
		}

		branchID := middleware.GetBranchID(c)
		results := buildVoiceInventoryResults(db, tenantID, branchID, items)

		c.JSON(http.StatusOK, voiceInventoryResponse{Data: results})
	}
}
