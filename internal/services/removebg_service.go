// Spec: specs/094-foto-fiel-fondo-realce/spec.md
//
// Recorte de fondo FIEL con un modelo dedicado de matting (remove.bg): devuelve el
// producto con sus PÍXELES REALES y fondo transparente — nunca re-dibuja ni altera el
// producto (a diferencia de un modelo generativo). La key vive en REMOVEBG_API_KEY
// (env de Render), nunca en el repo.
package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
)

// RemoveBackgroundConfigured indica si hay API key para el recorte fiel.
func RemoveBackgroundConfigured() bool {
	return strings.TrimSpace(os.Getenv("REMOVEBG_API_KEY")) != ""
}

// RemoveBackground envía la foto a remove.bg y devuelve un PNG con fondo transparente
// (el producto real recortado al pixel). Error si no hay key o si la API falla.
func RemoveBackground(ctx context.Context, imageData []byte) ([]byte, error) {
	key := strings.TrimSpace(os.Getenv("REMOVEBG_API_KEY"))
	if key == "" {
		return nil, fmt.Errorf("REMOVEBG_API_KEY no configurada")
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("size", "auto")
	_ = w.WriteField("format", "png") // PNG = conserva transparencia
	fw, err := w.CreateFormFile("image_file", "product.png")
	if err != nil {
		return nil, fmt.Errorf("no se pudo preparar la imagen: %w", err)
	}
	if _, err := fw.Write(imageData); err != nil {
		return nil, fmt.Errorf("no se pudo escribir la imagen: %w", err)
	}
	_ = w.Close()

	reqCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		"https://api.remove.bg/v1.0/removebg", &buf)
	if err != nil {
		return nil, fmt.Errorf("no se pudo crear la petición de recorte: %w", err)
	}
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error al llamar remove.bg: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error al leer respuesta de recorte: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, fmt.Errorf("remove.bg %d: %s", resp.StatusCode, snippet)
	}
	return body, nil
}
