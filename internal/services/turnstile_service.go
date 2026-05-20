// Spec: specs/024-captcha-registro-login/spec.md
//
// TurnstileService valida tokens de Cloudflare Turnstile llamando al
// endpoint siteverify de Cloudflare. El cliente HTTP es inyectable para
// poder mockear el servidor en tests sin usar red real (AC-07, D5).
//
// Uso:
//
//	svc := services.NewTurnstileService(secretKey, verifyURL, httpClient)
//	ok, err := svc.Verify(ctx, token, remoteIP)
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// TurnstileVerifyURL es el endpoint oficial de Cloudflare para siteverify.
// Se puede sobreescribir vía la variable de entorno TURNSTILE_VERIFY_URL
// (útil en tests de integración o staging).
const TurnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// TurnstileService verifica tokens de Cloudflare Turnstile contra el
// endpoint siteverify. El secreto nunca llega al frontend (Art. VI).
type TurnstileService struct {
	secretKey  string
	verifyURL  string
	httpClient *http.Client
}

// NewTurnstileService crea el servicio. El parámetro verifyURL permite
// apuntar a un servidor mock en tests; pasar TurnstileVerifyURL para
// producción. El httpClient es inyectable para controlar timeouts (el
// valor recomendado es 5 s).
func NewTurnstileService(secretKey, verifyURL string, httpClient *http.Client) *TurnstileService {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * 1e9} // 5 s en nanosegundos
	}
	return &TurnstileService{
		secretKey:  secretKey,
		verifyURL:  verifyURL,
		httpClient: httpClient,
	}
}

// siteverifyResponse es la respuesta JSON del endpoint de Cloudflare.
type siteverifyResponse struct {
	Success bool `json:"success"`
}

// Verify envía el token a Cloudflare y retorna (true, nil) si es válido.
//
// Retorna (false, error) cuando:
//   - token está vacío (sin llamar a Cloudflare)
//   - la red falla o el contexto expira
//   - Cloudflare responde con un código != 200
//
// Retorna (false, nil) cuando Cloudflare responde 200 pero success:false
// (token inválido, expirado o ya consumido).
func (s *TurnstileService) Verify(ctx context.Context, token, remoteIP string) (bool, error) {
	if token == "" {
		return false, fmt.Errorf("captcha token vacío")
	}

	formData := url.Values{}
	formData.Set("secret", s.secretKey)
	formData.Set("response", token)
	if remoteIP != "" {
		formData.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		s.verifyURL,
		strings.NewReader(formData.Encode()),
	)
	if err != nil {
		return false, fmt.Errorf("error creando request a siteverify: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("error llamando a siteverify: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("siteverify respondió con status %d", resp.StatusCode)
	}

	var result siteverifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("error decodificando respuesta de siteverify: %w", err)
	}

	return result.Success, nil
}
