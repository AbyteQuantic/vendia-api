// Spec: hotfix observabilidad de jobs de IA — antes los 8 fallos del
// último día mostraban exactamente el mismo mensaje genérico
// (aiJobGenericFailMessage), tapando la causa real (safety filter de
// Gemini, modelo respondió texto en vez de imagen, 429, etc.). Este
// archivo agrega un clasificador que mapea el error del worker a un
// mensaje en español específico para el tendero (Art. V) sin filtrar
// detalles técnicos (Art. VI).
package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// T-CLASS-01 — un error que reporta que Gemini devolvió texto en vez
// de imagen (caso típico cuando el modelo "explica" por qué no genera)
// se mapea a un mensaje que invita al tendero a darle más contexto.
func TestClassifyAIJobError_TextInsteadOfImage(t *testing.T) {
	err := errors.New(
		"error al generar imagen: gemini returned text instead of image: " +
			"I cannot create an image of tobacco products...",
	)
	msg := classifyAIJobError(err)
	assert.NotEqual(t, aiJobGenericFailMessage, msg)
	assert.Contains(t, strings.ToLower(msg), "nombre")
	assert.NotContains(t, msg, "gemini", "no debe filtrar nombre del proveedor")
	assert.NotContains(t, msg, "tobacco", "no debe filtrar el texto crudo")
}

// T-CLASS-02 — un safety block explícito de Gemini (códigos PROHIBITED_
// CONTENT, SAFETY) se mapea a un mensaje que indica que la categoría
// del producto no es soportada — útil para productos de tabaco,
// alcohol, medicamentos.
func TestClassifyAIJobError_SafetyBlock(t *testing.T) {
	for _, raw := range []string{
		"gemini API returned 400: {\"error\":{\"status\":\"PROHIBITED_CONTENT\"}}",
		"gemini error 400: blocked by safety filter",
		"gemini API returned 400: candidate.finishReason=SAFETY",
	} {
		msg := classifyAIJobError(errors.New(raw))
		assert.NotEqual(t, aiJobGenericFailMessage, msg, raw)
		assert.Contains(t, strings.ToLower(msg), "categoría", raw)
		assert.NotContains(t, msg, "gemini", raw)
	}
}

// T-CLASS-03 — rate limit (429) y RESOURCE_EXHAUSTED se mapean al
// mismo mensaje de "servicio ocupado, espere".
func TestClassifyAIJobError_RateLimit(t *testing.T) {
	for _, raw := range []string{
		"gemini API returned 429: rate limit exceeded",
		"gemini error 429: too many requests",
		"gemini API returned 429: {\"error\":{\"status\":\"RESOURCE_EXHAUSTED\"}}",
	} {
		msg := classifyAIJobError(errors.New(raw))
		assert.NotEqual(t, aiJobGenericFailMessage, msg, raw)
		assert.Contains(t, strings.ToLower(msg), "ocupado", raw)
	}
}

// T-CLASS-04 — fallo en descargar la foto fuente (R2 lento, URL 4xx/5xx)
// se mapea a un mensaje que diferencia del problema de la IA misma.
func TestClassifyAIJobError_DownloadFailed(t *testing.T) {
	for _, raw := range []string{
		"error al obtener foto: read tcp i/o timeout",
		"error al leer foto: unexpected EOF",
		"la URL de la foto devolvió 503",
	} {
		msg := classifyAIJobError(errors.New(raw))
		assert.NotEqual(t, aiJobGenericFailMessage, msg, raw)
		assert.Contains(t, strings.ToLower(msg), "foto original", raw)
	}
}

// T-CLASS-05 — fallo en subir la imagen generada al storage (R2)
// se mapea a un mensaje distinto del fallo de IA, para que el tendero
// no piense que el problema es su producto.
func TestClassifyAIJobError_UploadFailed(t *testing.T) {
	err := errors.New("error al guardar foto mejorada: r2 PutObject failed: 503")
	msg := classifyAIJobError(err)
	assert.NotEqual(t, aiJobGenericFailMessage, msg)
	assert.Contains(t, strings.ToLower(msg), "guardar")
}

// T-CLASS-06 — el caso "no image returned" del candidate vacío de
// Gemini se mapea similar al texto-en-vez-de-imagen.
func TestClassifyAIJobError_NoImageReturned(t *testing.T) {
	err := errors.New("error al generar imagen: no image returned from Gemini (candidates=0)")
	msg := classifyAIJobError(err)
	assert.NotEqual(t, aiJobGenericFailMessage, msg)
	assert.Contains(t, strings.ToLower(msg), "no pudo")
}

// T-CLASS-07 — un error desconocido cae al mensaje genérico (preserva
// el contrato anterior).
func TestClassifyAIJobError_UnknownFallsBackToGeneric(t *testing.T) {
	err := errors.New("some unrecognized error from elsewhere")
	msg := classifyAIJobError(err)
	assert.Equal(t, aiJobGenericFailMessage, msg)
}

// T-CLASS-08 — timeout debe seguir teniendo precedencia y manejarse
// fuera del clasificador (sigue siendo responsabilidad de runAIJob).
// Acá solo confirmamos que un context.DeadlineExceeded crudo entra al
// fallback genérico — runAIJob es quien decide el mensaje de timeout.
func TestClassifyAIJobError_TimeoutFallsBack(t *testing.T) {
	msg := classifyAIJobError(context.DeadlineExceeded)
	assert.Equal(t, aiJobGenericFailMessage, msg)
}

// T-CLASS-09 — el mensaje retornado NUNCA debe ser vacío ni igual al
// error crudo (defensa contra cualquier branch nuevo que se olvide de
// retornar algo).
func TestClassifyAIJobError_NeverEmptyNeverRaw(t *testing.T) {
	cases := []error{
		errors.New("gemini returned text instead of image: foo"),
		errors.New("gemini API returned 429"),
		errors.New("la URL de la foto devolvió 503"),
		errors.New("totally unknown"),
		errors.New(""),
	}
	for _, e := range cases {
		msg := classifyAIJobError(e)
		assert.NotEmpty(t, msg, e)
		assert.NotEqual(t, e.Error(), msg, e)
	}
}
