// Spec: specs/057-panic-button-delivery/spec.md
//
// Envío real de alertas de pánico por SMS (Twilio) y WhatsApp (Meta
// Cloud API). Es fail-closed: si el canal no está configurado por env,
// devuelve `skipped` sin intentar nada (no rompe el trigger ni miente
// diciendo "enviado"). El handler persiste el resultado por contacto.
package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Estados de entrega (espejo de models.SosAlertDelivery.Status).
const (
	DeliverySent    = "sent"
	DeliveryFailed  = "failed"
	DeliverySkipped = "skipped"
)

// DeliveryResult es el desenlace de un intento de envío a un contacto.
type DeliveryResult struct {
	Status        string
	ProviderMsgID string
	Error         string
}

// EmergencyNotifier envía por SMS/WhatsApp leyendo credenciales del
// entorno. Construir es barato; léase por request.
type EmergencyNotifier struct {
	httpClient *http.Client

	twilioSID   string
	twilioToken string
	twilioFrom  string

	metaPhoneID  string
	metaToken    string
	metaTemplate string
	metaLang     string
}

// NewEmergencyNotifier lee las env vars de Twilio y Meta. Faltantes →
// el canal queda "no configurado" (fail-closed).
func NewEmergencyNotifier() *EmergencyNotifier {
	lang := os.Getenv("META_WA_TEMPLATE_LANG")
	if lang == "" {
		lang = "es"
	}
	return &EmergencyNotifier{
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		twilioSID:    os.Getenv("TWILIO_ACCOUNT_SID"),
		twilioToken:  os.Getenv("TWILIO_AUTH_TOKEN"),
		twilioFrom:   os.Getenv("TWILIO_FROM_NUMBER"),
		metaPhoneID:  os.Getenv("META_WA_PHONE_ID"),
		metaToken:    os.Getenv("META_WA_TOKEN"),
		metaTemplate: os.Getenv("META_WA_TEMPLATE"),
		metaLang:     lang,
	}
}

// SMSConfigured indica si Twilio está listo para enviar.
func (n *EmergencyNotifier) SMSConfigured() bool {
	return n.twilioSID != "" && n.twilioToken != "" && n.twilioFrom != ""
}

// WhatsAppConfigured indica si Meta Cloud API está listo.
func (n *EmergencyNotifier) WhatsAppConfigured() bool {
	return n.metaPhoneID != "" && n.metaToken != "" && n.metaTemplate != ""
}

var nonDigit = regexp.MustCompile(`\D`)
var whitespaceRun = regexp.MustCompile(`\s+`)

// sanitizeTemplateParam adapta el cuerpo a lo que acepta un parámetro de
// plantilla de Meta: SIN saltos de línea, tabs ni runs de ≥4 espacios.
// Colapsa todo run de whitespace a un solo espacio y separa los bloques
// (mensaje · dirección · ubicación) con " — ".
func sanitizeTemplateParam(s string) string {
	s = strings.ReplaceAll(s, "\n\n", " — ")
	s = strings.ReplaceAll(s, "\n", " — ")
	s = whitespaceRun.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// NormalizeCoPhone deja solo dígitos y antepone 57 a celulares CO de 10
// dígitos que empiezan por 3. Para SMS/WhatsApp internacional se usa con
// `+` (Twilio) o solo dígitos (Meta).
func NormalizeCoPhone(raw string) string {
	digits := nonDigit.ReplaceAllString(raw, "")
	if len(digits) == 10 && strings.HasPrefix(digits, "3") {
		digits = "57" + digits
	}
	return digits
}

// Dispatch enruta al canal del contacto. Cualquier método distinto de
// "sms" cae a WhatsApp (es el default histórico de EmergencyContact).
func (n *EmergencyNotifier) Dispatch(method, phone, body string) DeliveryResult {
	if method == "sms" {
		return n.SendSMS(phone, body)
	}
	return n.SendWhatsApp(phone, body)
}

// SendSMS envía por Twilio. Fail-closed → skipped si no hay credenciales.
func (n *EmergencyNotifier) SendSMS(phone, body string) DeliveryResult {
	if !n.SMSConfigured() {
		return DeliveryResult{Status: DeliverySkipped, Error: "SMS (Twilio) no configurado"}
	}
	to := "+" + NormalizeCoPhone(phone)
	endpoint := fmt.Sprintf(
		"https://api.twilio.com/2010-04-01/Accounts/%s/Messages.json", n.twilioSID)
	form := url.Values{}
	form.Set("From", n.twilioFrom)
	form.Set("To", to)
	form.Set("Body", body)

	req, err := http.NewRequest(http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return DeliveryResult{Status: DeliveryFailed, Error: err.Error()}
	}
	req.SetBasicAuth(n.twilioSID, n.twilioToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return DeliveryResult{Status: DeliveryFailed, Error: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		// El detalle crudo del proveedor va a logs del server, NO al
		// cliente (lo ve un tendero 50+ en el histórico).
		log.Printf("[PANIC-SMS] twilio %d: %s", resp.StatusCode, truncateErr(string(raw), 300))
		return DeliveryResult{Status: DeliveryFailed,
			Error: "No se pudo enviar el SMS"}
	}
	var parsed struct {
		SID string `json:"sid"`
	}
	_ = json.Unmarshal(raw, &parsed)
	return DeliveryResult{Status: DeliverySent, ProviderMsgID: parsed.SID}
}

// SendWhatsApp envía por Meta Cloud API usando una plantilla aprobada
// con un parámetro de cuerpo {{1}} = mensaje completo. Fail-closed.
func (n *EmergencyNotifier) SendWhatsApp(phone, body string) DeliveryResult {
	if !n.WhatsAppConfigured() {
		return DeliveryResult{Status: DeliverySkipped, Error: "WhatsApp (Meta) no configurado"}
	}
	to := NormalizeCoPhone(phone)
	endpoint := fmt.Sprintf("https://graph.facebook.com/v21.0/%s/messages", n.metaPhoneID)

	// Meta rechaza parámetros de plantilla con saltos de línea / tabs /
	// runs de ≥4 espacios. El mensaje de pánico trae \n\n + URL de Maps,
	// así que lo aplanamos para que NO falle toda la entrega de WhatsApp.
	param := sanitizeTemplateParam(body)
	payload := map[string]any{
		"messaging_product": "whatsapp",
		"to":                to,
		"type":              "template",
		"template": map[string]any{
			"name":     n.metaTemplate,
			"language": map[string]any{"code": n.metaLang},
			"components": []any{
				map[string]any{
					"type": "body",
					"parameters": []any{
						map[string]any{"type": "text", "text": param},
					},
				},
			},
		},
	}
	buf, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return DeliveryResult{Status: DeliveryFailed, Error: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+n.metaToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return DeliveryResult{Status: DeliveryFailed, Error: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		log.Printf("[PANIC-WA] meta %d: %s", resp.StatusCode, truncateErr(string(raw), 300))
		return DeliveryResult{Status: DeliveryFailed,
			Error: "No se pudo enviar el WhatsApp"}
	}
	var parsed struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(raw, &parsed)
	id := ""
	if len(parsed.Messages) > 0 {
		id = parsed.Messages[0].ID
	}
	return DeliveryResult{Status: DeliverySent, ProviderMsgID: id}
}

func truncateErr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
