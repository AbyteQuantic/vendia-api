// Spec: specs/038-push-notifications-web-android/spec.md
//
// Paquete `push` aísla la integración con FCM y Web Push (RFC 8030)
// detrás de una interfaz pequeña (`Sender`) que el dispatcher consume.
//
// Por qué dos protocolos:
//   - FCM: lo que usábamos en Android nativo + Web (Chrome, Firefox).
//     firebase_messaging genera un token, backend envía vía Firebase
//     Admin SDK.
//   - Web Push nativo (RFC 8291): el browser registra un endpoint
//     directamente con su propio servicio (Apple para iOS Safari,
//     Mozilla, etc.). Backend firma con VAPID y envía con webpush-go.
//     Necesario porque firebase_messaging FALLA en Flutter web +
//     iOS Safari con un PlatformException(channel-error) sin
//     solución upstream a 2026-05-29.
//
// Constitución:
//   - Art. VI: credenciales en env de Render (FCM_SERVICE_ACCOUNT_JSON,
//     VAPID_PRIVATE_KEY), NUNCA en repo ni en logs.
//   - Art. IX: archivo enfocado, los dos protocolos viven en helpers
//     separados acá pero el `Sender` los unifica para el dispatcher.
package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	webpush "github.com/SherClockHolmes/webpush-go"
	"google.golang.org/api/option"
)

// Target describe un destino genérico de push. El sender mira los
// campos para decidir el protocolo: si `FCMToken` está lleno → FCM;
// si `Endpoint` está lleno → Web Push nativo.
//
// `DeviceID` es el UUID del row en `device_tokens` y se usa para
// que el sender reporte cuáles devices quedaron inválidos. Así el
// dispatcher invalida por id (portable entre protocolos), no por
// token (FCM token vs URL distinguibles).
type Target struct {
	DeviceID string
	FCMToken string
	Endpoint string
	P256dh   string
	Auth     string
}

// Payload describe el contenido a entregar al sistema operativo del
// dispositivo. Es inmutable desde la perspectiva del sender.
type Payload struct {
	Title    string
	Body     string
	DeepLink string
}

// SendResult resume el efecto de un Send. `Invalid` lleva los
// `DeviceID` (NO los tokens) que el proveedor reportó como muertos
// — el dispatcher pone `invalidated_at` por id.
type SendResult struct {
	Sent    int
	Invalid []string // device IDs
}

// Sender es la abstracción consumida por el dispatcher.
type Sender interface {
	Send(ctx context.Context, targets []Target, payload Payload) (SendResult, error)
}

// ─── FakeSender (tests) ──────────────────────────────────────────────

// FakeSender captura cada llamada para que los tests assertean.
type FakeSender struct {
	Calls []FakeCall
	// InvalidateDeviceIDs es el set de device_ids que el fake reporta
	// como inválidos. Default vacío.
	InvalidateDeviceIDs map[string]bool
	NextError           error
}

type FakeCall struct {
	Targets []Target
	Payload Payload
}

func (f *FakeSender) Send(ctx context.Context, targets []Target, payload Payload) (SendResult, error) {
	if err := f.NextError; err != nil {
		f.NextError = nil
		return SendResult{}, err
	}
	if len(targets) == 0 {
		return SendResult{}, nil
	}
	f.Calls = append(f.Calls, FakeCall{Targets: targets, Payload: payload})

	var invalid []string
	sent := 0
	for _, t := range targets {
		if f.InvalidateDeviceIDs[t.DeviceID] {
			invalid = append(invalid, t.DeviceID)
			continue
		}
		sent++
	}
	return SendResult{Sent: sent, Invalid: invalid}, nil
}

// ─── UnifiedSender (producción) ──────────────────────────────────────

// UnifiedSender es el sender de producción: rutea cada Target al
// protocolo correcto. Si FCMToken está lleno → FCM Admin SDK. Si
// Endpoint está lleno → Web Push protocol con VAPID.
//
// Se construye una sola vez en cmd/server/main.go con NewUnifiedSender
// y se inyecta al dispatcher.
type UnifiedSender struct {
	fcmClient    *messaging.Client // nil si FCM no está configurado
	vapidPublic  string            // "" si Web Push no está configurado
	vapidPrivate string
	vapidSubject string
}

// FCMConfig agrupa lo necesario para el cliente FCM.
type FCMConfig struct {
	ServiceAccountJSON string
	ProjectID          string
}

// VAPIDConfig agrupa lo necesario para Web Push nativo.
type VAPIDConfig struct {
	PublicKey  string
	PrivateKey string
	Subject    string // mailto:contacto@vendia.store
}

// NewUnifiedSender intenta inicializar ambos backends. Si uno falla
// (env vars vacías), el sender funciona sin él: el dispatcher solo
// puede enviar a targets del protocolo configurado. Falla solo si
// AMBOS están vacíos.
func NewUnifiedSender(ctx context.Context, fcm FCMConfig, vapid VAPIDConfig) (*UnifiedSender, error) {
	s := &UnifiedSender{}

	if fcm.ServiceAccountJSON != "" && fcm.ProjectID != "" {
		creds := option.WithCredentialsJSON([]byte(fcm.ServiceAccountJSON))
		app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: fcm.ProjectID}, creds)
		if err != nil {
			return nil, fmt.Errorf("push: firebase init: %w", err)
		}
		client, err := app.Messaging(ctx)
		if err != nil {
			return nil, fmt.Errorf("push: messaging client: %w", err)
		}
		s.fcmClient = client
	}

	if vapid.PublicKey != "" && vapid.PrivateKey != "" {
		s.vapidPublic = vapid.PublicKey
		s.vapidPrivate = vapid.PrivateKey
		s.vapidSubject = vapid.Subject
		if s.vapidSubject == "" {
			s.vapidSubject = "mailto:contacto@vendia.store"
		}
	}

	if s.fcmClient == nil && s.vapidPublic == "" {
		return nil, errors.New(
			"push: ningún backend configurado " +
				"(falta FCM_SERVICE_ACCOUNT_JSON o VAPID_PUBLIC_KEY/VAPID_PRIVATE_KEY)")
	}
	return s, nil
}

// Send rutea los targets a cada backend según el protocolo y agrega
// los resultados.
func (s *UnifiedSender) Send(ctx context.Context, targets []Target, payload Payload) (SendResult, error) {
	if len(targets) == 0 {
		return SendResult{}, nil
	}

	// Split: FCM vs Web Push.
	var fcmTargets, webPushTargets []Target
	for _, t := range targets {
		if t.FCMToken != "" {
			fcmTargets = append(fcmTargets, t)
		} else if t.Endpoint != "" {
			webPushTargets = append(webPushTargets, t)
		}
	}

	var combined SendResult
	if len(fcmTargets) > 0 {
		r, err := s.sendFCM(ctx, fcmTargets, payload)
		if err != nil {
			return combined, err
		}
		combined.Sent += r.Sent
		combined.Invalid = append(combined.Invalid, r.Invalid...)
	}
	if len(webPushTargets) > 0 {
		r, err := s.sendWebPush(ctx, webPushTargets, payload)
		if err != nil {
			return combined, err
		}
		combined.Sent += r.Sent
		combined.Invalid = append(combined.Invalid, r.Invalid...)
	}
	return combined, nil
}

func (s *UnifiedSender) sendFCM(ctx context.Context, targets []Target, payload Payload) (SendResult, error) {
	if s.fcmClient == nil {
		// FCM no configurado. NO invalidar los devices — son válidos,
		// solo nos falta config nuestra. Reportar 0 sent y dejar los
		// devices intactos. El operador (Bryan) ve el warning en
		// logs y agrega FCM_SERVICE_ACCOUNT_JSON cuando pueda.
		log.Printf("[PUSH] %d FCM target(s) descartados — FCM no configurado en este server", len(targets))
		return SendResult{}, nil
	}

	tokens := make([]string, len(targets))
	for i, t := range targets {
		tokens[i] = t.FCMToken
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
	resp, err := s.fcmClient.SendEachForMulticast(ctx, msg)
	if err != nil {
		return SendResult{}, fmt.Errorf("push: FCM SendEachForMulticast: %w", err)
	}
	var invalid []string
	sent := 0
	for i, r := range resp.Responses {
		if r.Success {
			sent++
			continue
		}
		if messaging.IsRegistrationTokenNotRegistered(r.Error) ||
			messaging.IsInvalidArgument(r.Error) {
			invalid = append(invalid, targets[i].DeviceID)
		}
	}
	return SendResult{Sent: sent, Invalid: invalid}, nil
}

func (s *UnifiedSender) sendWebPush(ctx context.Context, targets []Target, payload Payload) (SendResult, error) {
	if s.vapidPublic == "" {
		// VAPID no configurado. NO invalidar los devices Web Push —
		// son válidos, falta la config (VAPID_PRIVATE_KEY/PUBLIC_KEY)
		// en Render. El operador agrega las env vars y reintenta.
		log.Printf("[PUSH] %d Web Push target(s) descartados — VAPID no configurado en este server", len(targets))
		return SendResult{}, nil
	}

	body, _ := json.Marshal(map[string]string{
		"title":     payload.Title,
		"body":      payload.Body,
		"deep_link": payload.DeepLink,
	})

	var invalid []string
	sent := 0
	for _, t := range targets {
		sub := &webpush.Subscription{
			Endpoint: t.Endpoint,
			Keys: webpush.Keys{
				P256dh: t.P256dh,
				Auth:   t.Auth,
			},
		}
		resp, err := webpush.SendNotificationWithContext(ctx, body, sub, &webpush.Options{
			Subscriber:      s.vapidSubject,
			VAPIDPublicKey:  s.vapidPublic,
			VAPIDPrivateKey: s.vapidPrivate,
			TTL:             60 * 60, // 1 hora
		})
		if err != nil {
			// Error de red transitorio — no invalidamos.
			continue
		}
		resp.Body.Close()
		// 404 (subscription expired) o 410 (gone) → invalidar.
		if resp.StatusCode == 404 || resp.StatusCode == 410 {
			invalid = append(invalid, t.DeviceID)
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			sent++
		}
	}
	return SendResult{Sent: sent, Invalid: invalid}, nil
}

// compile-time check.
var (
	_ Sender = (*FakeSender)(nil)
	_ Sender = (*UnifiedSender)(nil)
)
