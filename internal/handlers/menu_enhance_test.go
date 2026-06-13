// Spec: specs/043-menu-restaurante-recetas/spec.md
package handlers

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// buildEnhanceMenuMultipart hand-rolls a multipart body with an optional
// `image` file part and an optional `name` text field, so each validation
// branch of EnhanceMenuImage can be exercised independently.
func buildEnhanceMenuMultipart(t *testing.T, withImage bool, imageBytes []byte, name string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	if withImage {
		hdr := map[string][]string{
			"Content-Disposition": {`form-data; name="image"; filename="plato.png"`},
			"Content-Type":        {"image/png"},
		}
		part, err := mw.CreatePart(hdr)
		require.NoError(t, err)
		_, err = part.Write(imageBytes)
		require.NoError(t, err)
	}
	if name != "" {
		require.NoError(t, mw.WriteField("name", name))
	}
	require.NoError(t, mw.Close())
	return body, mw.FormDataContentType()
}

func doEnhanceMenu(t *testing.T, gemini *services.GeminiService, storage services.FileStorage, body *bytes.Buffer, ctype string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/menu/enhance-image", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, "tenant-1")
		EnhanceMenuImage(gemini, storage)(c)
	})
	req := httptest.NewRequest(http.MethodPost, "/menu/enhance-image", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Sin servicios de IA configurados → 503 (mismo contrato que GenerateMenuImage).
func TestEnhanceMenuImage_NilServices(t *testing.T) {
	body, ctype := buildEnhanceMenuMultipart(t, true, pngBytes, "Bandeja Paisa")
	w := doEnhanceMenu(t, nil, nil, body, ctype)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
}

// Sin el campo `image` → 400.
func TestEnhanceMenuImage_MissingImage(t *testing.T) {
	gemini := services.NewGeminiService("k", "m", "im", time.Second)
	body, ctype := buildEnhanceMenuMultipart(t, false, nil, "Bandeja Paisa")
	w := doEnhanceMenu(t, gemini, newFakeStorage(), body, ctype)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// Con imagen pero sin nombre (o muy corto) → 400: la mejora necesita el
// nombre como contexto del plato.
func TestEnhanceMenuImage_MissingName(t *testing.T) {
	gemini := services.NewGeminiService("k", "m", "im", time.Second)
	body, ctype := buildEnhanceMenuMultipart(t, true, pngBytes, "")
	w := doEnhanceMenu(t, gemini, newFakeStorage(), body, ctype)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// Imagen mayor a 8MB → 400 con mensaje amable (no se confía solo en el cliente).
func TestEnhanceMenuImage_TooLarge(t *testing.T) {
	gemini := services.NewGeminiService("k", "m", "im", time.Second)
	big := make([]byte, (8<<20)+1)
	copy(big, pngBytes) // cabecera PNG válida; el tamaño es lo que se rechaza
	body, ctype := buildEnhanceMenuMultipart(t, true, big, "Bandeja Paisa")
	w := doEnhanceMenu(t, gemini, newFakeStorage(), body, ctype)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}
