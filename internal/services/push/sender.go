// Spec: specs/038-push-notifications-web-android/spec.md
//
// Paquete `push` aísla la integración con FCM detrás de una interfaz
// pequeña (`Sender`) que el dispatcher consume. Esto permite:
//   - Tests unitarios sin red (FakeSender capturando llamadas).
//   - Inyectar credenciales reales en producción vía
//     `NewFCMSender(...)` desde `cmd/server/main.go`.
//   - Sustituir FCM por otro proveedor (OneSignal, Pusher) en el
//     futuro sin tocar el dispatcher.
//
// Constitución:
//   - Art. VI: las credenciales (`FCM_SERVICE_ACCOUNT_JSON`) viven en
//     env de Render, nunca en el repo, y el token de cada dispositivo
//     se manipula como bytes opacos — NO se loguea en plano.
//   - Art. IX: archivo enfocado < 200 LOC; el dispatcher (siguiente
//     archivo) usa esta interfaz sin conocer FCM directamente.
package push

import (
	"context"
	"errors"
	"fmt"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
)

// Payload describe el contenido a entregar al sistema operativo del
// dispositivo. Es inmutable desde la perspectiva del sender — nunca se
// muta después de pasarla a Send.
type Payload struct {
	Title    string
	Body     string
	DeepLink string // Opcional. Si no está vacío, el cliente Flutter lo lee del data payload y navega a esa ruta.
}

// SendResult resume el efecto de un Send. El dispatcher consume:
//   - `Sent`: cuántos tokens recibieron OK (usa para decidir si
//     `pushed_at` se setea en la Notification).
//   - `Invalid`: lista de tokens que FCM marcó como inválidos
//     (`UNREGISTERED`, `INVALID_ARGUMENT`); el dispatcher pone
//     `invalidated_at` en esos rows (AC-10).
type SendResult struct {
	Sent    int
	Invalid []string
}

// Sender es la abstracción consumida por el dispatcher. Todas las
// implementaciones deben respetar la cancelación del ctx — un
// dispatcher de prueba puede pasar un ctx con deadline corto para
// validar timeouts.
type Sender interface {
	Send(ctx context.Context, tokens []string, payload Payload) (SendResult, error)
}

// ─── FakeSender (tests) ──────────────────────────────────────────────

// FakeSender captura cada llamada para que los tests del dispatcher
// puedan assertear quién envió qué a quién. También permite simular
// tokens inválidos y errores transitorios.
type FakeSender struct {
	// Calls acumula cada Send. Tests inspeccionan
	// `fake.Calls[0].Tokens`, `fake.Calls[0].Payload`.
	Calls []FakeCall

	// InvalidateTokens es un set de tokens que el fake debe
	// reportar como inválidos en el SendResult. Default vacío.
	InvalidateTokens map[string]bool

	// NextError, si no es nil, se retorna en la siguiente llamada y
	// se limpia (one-shot). Permite simular un fallo transitorio.
	NextError error
}

type FakeCall struct {
	Tokens  []string
	Payload Payload
}

func (f *FakeSender) Send(ctx context.Context, tokens []string, payload Payload) (SendResult, error) {
	if err := f.NextError; err != nil {
		f.NextError = nil
		return SendResult{}, err
	}
	if len(tokens) == 0 {
		return SendResult{}, nil
	}
	f.Calls = append(f.Calls, FakeCall{Tokens: tokens, Payload: payload})

	var invalid []string
	sent := 0
	for _, t := range tokens {
		if f.InvalidateTokens[t] {
			invalid = append(invalid, t)
			continue
		}
		sent++
	}
	return SendResult{Sent: sent, Invalid: invalid}, nil
}

// ─── FCMSender (producción) ──────────────────────────────────────────

// FCMConfig agrupa lo necesario para inicializar el cliente FCM.
// `ServiceAccountJSON` es el contenido COMPLETO del JSON descargado
// del Firebase Console — no la ruta a un archivo. Llega vía env var
// `FCM_SERVICE_ACCOUNT_JSON` en Render.
type FCMConfig struct {
	ServiceAccountJSON string
	ProjectID          string
}

// FCMSender envuelve `firebase.google.com/go/v4/messaging`. Se
// construye una sola vez en `cmd/server/main.go` y se inyecta al
// dispatcher.
type FCMSender struct {
	client *messaging.Client
}

// NewFCMSender inicializa el cliente FCM. Falla si el JSON está vacío
// (defensa: en arranque local sin env, el server debe usar
// FakeSender en su lugar — no un FCMSender con nil-client que se
// crashe en runtime).
func NewFCMSender(ctx context.Context, cfg FCMConfig) (*FCMSender, error) {
	if cfg.ServiceAccountJSON == "" {
		return nil, errors.New("push: FCM_SERVICE_ACCOUNT_JSON vacío — configurar env var en Render")
	}
	if cfg.ProjectID == "" {
		return nil, errors.New("push: FCM_PROJECT_ID vacío")
	}

	creds := option.WithCredentialsJSON([]byte(cfg.ServiceAccountJSON))
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: cfg.ProjectID}, creds)
	if err != nil {
		return nil, fmt.Errorf("push: firebase init: %w", err)
	}
	client, err := app.Messaging(ctx)
	if err != nil {
		return nil, fmt.Errorf("push: messaging client: %w", err)
	}
	return &FCMSender{client: client}, nil
}

// Send envía la misma `payload` a todos los tokens via FCM's
// SendEachForMulticast (replacement de SendMulticast deprecado en v4
// del SDK). Recolecta los tokens inválidos para que el dispatcher los
// marque y deje de reintentarlos.
func (s *FCMSender) Send(ctx context.Context, tokens []string, payload Payload) (SendResult, error) {
	if len(tokens) == 0 {
		return SendResult{}, nil
	}

	data := map[string]string{}
	if payload.DeepLink != "" {
		data["deep_link"] = payload.DeepLink
	}

	msg := &messaging.MulticastMessage{
		Tokens: tokens,
		Notification: &messaging.Notification{
			Title: payload.Title,
			Body:  payload.Body,
		},
		Data: data,
	}

	resp, err := s.client.SendEachForMulticast(ctx, msg)
	if err != nil {
		return SendResult{}, fmt.Errorf("push: SendEachForMulticast: %w", err)
	}

	var invalid []string
	sent := 0
	for i, r := range resp.Responses {
		if r.Success {
			sent++
			continue
		}
		// Tokens que FCM marca como muertos — el dispatcher los
		// invalida en la BD para no reintentar.
		if messaging.IsRegistrationTokenNotRegistered(r.Error) ||
			messaging.IsInvalidArgument(r.Error) {
			invalid = append(invalid, tokens[i])
		}
		// Otros errores (5xx de Google, red transitoria) no
		// invalidan el token — quedan para reintento en el próximo
		// evento.
	}

	return SendResult{Sent: sent, Invalid: invalid}, nil
}

// compile-time check de que ambos implementan Sender.
var (
	_ Sender = (*FakeSender)(nil)
	_ Sender = (*FCMSender)(nil)
)
