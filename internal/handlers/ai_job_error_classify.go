// Spec: hotfix observabilidad de jobs de IA — antes runAIJob escribía
// SIEMPRE el mismo aiJobGenericFailMessage en error_message,
// independientemente de la causa real. Resultado: 8 fallos en 24h
// imposibles de diagnosticar sin acceso a logs de Render. Este archivo
// mapea cada categoría de fallo a un mensaje en español específico
// para el tendero, manteniendo Art. V (español) y Art. VI (no filtrar
// detalles técnicos crudos del proveedor).
package handlers

import (
	"strings"

	"vendia-backend/internal/services"
)

// Mensajes en español. Cada uno corresponde a una categoría
// reconocible del worker que se puede accionar por el tendero
// (intentar otra foto, esperar, etc.).
const (
	aiErrSafetyMessage = "La IA no puede crear imágenes para este tipo " +
		"de producto por su categoría. Intente subir una foto del " +
		"producto en buena luz."
	aiErrTextResponseMessage = "La IA no entendió bien este producto. " +
		"Intente con un nombre más específico o agregue una foto."
	aiErrNoImageMessage = "La IA no pudo crear la imagen esta vez. " +
		"Intente de nuevo o use una foto del producto."
	aiErrRateLimitMessage = "El servicio de IA está ocupado en este " +
		"momento. Espere un minuto y vuelva a intentar."
	aiErrDownloadMessage = "No pudimos descargar la foto original. " +
		"Verifique su conexión y vuelva a intentar."
	aiErrUploadMessage = "No pudimos guardar la imagen generada. " +
		"Vuelva a intentar en un momento."
)

// classifyAIJobError traduce el error técnico del worker a un mensaje
// en español para el tendero, sin filtrar nombres de proveedor, stack
// traces, ni el texto crudo que Gemini pudo haber devuelto.
//
// La función nunca retorna vacío: cualquier error no reconocido cae al
// aiJobGenericFailMessage existente, manteniendo el contrato anterior
// para los casos no clasificados.
//
// El timeout (context.DeadlineExceeded) NO se clasifica acá — esa
// distinción la sigue haciendo runAIJob antes de llamarnos, mapeando
// directo a aiTimeoutMessage. Acá un timeout que se filtra cae al
// genérico (es bug aguas arriba si llega así).
func classifyAIJobError(err error) string {
	if err == nil {
		return aiJobGenericFailMessage
	}
	raw := strings.ToLower(err.Error())

	// Orden importante: los patrones más específicos antes que los
	// más amplios. Un "gemini API returned 400" puede ser safety o
	// otra cosa — el chequeo de safety va primero porque mira
	// keywords adicionales.

	if isSafetyBlock(raw) {
		return aiErrSafetyMessage
	}
	if strings.Contains(raw, "returned text instead of image") {
		return aiErrTextResponseMessage
	}
	if strings.Contains(raw, "no image returned") {
		return aiErrNoImageMessage
	}
	if isRateLimit(raw) {
		return aiErrRateLimitMessage
	}
	if isDownloadFailure(raw) {
		return aiErrDownloadMessage
	}
	if isUploadFailure(raw) {
		return aiErrUploadMessage
	}

	return aiJobGenericFailMessage
}

func isSafetyBlock(raw string) bool {
	// Patrones típicos de Gemini cuando rechaza por safety:
	//   - status: "PROHIBITED_CONTENT" / "SAFETY"
	//   - finishReason: "SAFETY" / "PROHIBITED_CONTENT"
	//   - texto suelto: "blocked by safety", "safety filter"
	return strings.Contains(raw, "prohibited_content") ||
		strings.Contains(raw, "finishreason=safety") ||
		strings.Contains(raw, "\"safety\"") ||
		strings.Contains(raw, "safety filter") ||
		strings.Contains(raw, "blocked by safety")
}

func isRateLimit(raw string) bool {
	// Fuente única de patrones 429: services.IsRateLimitMessage — la
	// comparte el backoff del worker de retoque (Spec 101, AC-10).
	return services.IsRateLimitMessage(raw)
}

func isDownloadFailure(raw string) bool {
	// Errores wrapped por downloadSourceImage (ver inventory.go).
	return strings.Contains(raw, "error al obtener foto") ||
		strings.Contains(raw, "error al leer foto") ||
		strings.Contains(raw, "la url de la foto devolvió") ||
		strings.Contains(raw, "error al preparar descarga")
}

func isUploadFailure(raw string) bool {
	// Errores wrapped por enhancePhotoWorker / generateImageWorker
	// cuando storageSvc.Upload falla.
	return strings.Contains(raw, "error al guardar foto") ||
		strings.Contains(raw, "error al guardar imagen")
}

// aiJobErrorCategory mapea el mensaje en español al slug en inglés
// usado en el log estructurado de runAIJob. Permite que el operador
// haga `grep "category=safety"` o `category=rate_limit` en Render.
// Devuelve "unknown" si el mensaje no coincide con ninguna categoría
// específica (cae al genérico).
func aiJobErrorCategory(msg string) string {
	switch msg {
	case aiErrSafetyMessage:
		return "safety"
	case aiErrTextResponseMessage:
		return "text_response"
	case aiErrNoImageMessage:
		return "no_image"
	case aiErrRateLimitMessage:
		return "rate_limit"
	case aiErrDownloadMessage:
		return "download_failed"
	case aiErrUploadMessage:
		return "upload_failed"
	default:
		return "unknown"
	}
}
