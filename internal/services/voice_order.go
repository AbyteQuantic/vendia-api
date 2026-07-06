// Spec: specs/085-vender-por-voz/spec.md
package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"vendia-backend/internal/models"
)

// VoiceOrderTarget — destino de la orden (mesa o cliente de mostrador).
type VoiceOrderTarget struct {
	Type    string `json:"type"` // mesa | cliente | mostrador
	Mesa    string `json:"mesa,omitempty"`
	Cliente string `json:"cliente,omitempty"`
}

// VoiceOrderCommand — una intención del tendero. La lista es ORDENADA: el
// front la aplica de arriba a abajo. El LLM NO conoce el catálogo; `Item` es el
// nombre HABLADO en minúsculas sin tildes, y el front lo resuelve a un producto
// real con foldKey (privacidad + offline + sin catalog-drift).
type VoiceOrderCommand struct {
	Action        string            `json:"action"` // agregar|quitar|fijar_cantidad|vaciar|fijar_mesa|fijar_cliente|cobrar
	Item          *string           `json:"item"`
	Quantity      *int              `json:"quantity"`
	Target        *VoiceOrderTarget `json:"target"`
	Confidence    float64           `json:"confidence"`
	ClarifyPrompt *string           `json:"clarify_prompt"`
	Raw           string            `json:"raw"`
}

// VoiceOrderResult — sobre que devuelve el handler. Siempre HTTP 200; ante
// fallo de IA → Degraded=true + Commands vacío (nunca rompe la venta).
type VoiceOrderResult struct {
	Commands      []VoiceOrderCommand `json:"commands"`
	Transcript    string              `json:"transcript"`
	ClarifyPrompt *string             `json:"clarify_prompt"`
	Degraded      bool                `json:"degraded"`
	Reason        string              `json:"reason,omitempty"`
}

// ValidVoiceActions — whitelist de acciones; cualquier otra se descarta.
var ValidVoiceActions = map[string]struct{}{
	"agregar":        {},
	"quitar":         {},
	"fijar_cantidad": {},
	"vaciar":         {},
	"fijar_mesa":     {},
	"fijar_cliente":  {},
	"cobrar":         {},
}

// VoiceOrderPrompt — prompt anti-alucinación (Gemini temp 0), diseñado por
// council (Spec 085). El modelo solo estructura el habla en comandos.
const VoiceOrderPrompt = `Usted es el asistente de VENTA POR VOZ de VendIA, un POS para tenderos colombianos. Su UNICA tarea es convertir lo que el tendero dijo en voz en una lista ORDENADA de COMANDOS de intencion para editar la orden actual (de una mesa o de un cliente de mostrador).

REGLAS ABSOLUTAS (no inventar):
1. NO invente productos. Copie el nombre del producto TAL CUAL lo dijo el tendero en el campo "item" (en minusculas, sin tildes). Usted NO conoce el catalogo: el sistema resolvera despues cual producto real es. Nunca corrija marcas, traduzca ni complete nombres.
2. Cantidades y operaciones LITERALES. Use solo los numeros que se escucharon; convierta numeros en palabras a digitos ("dos"->2, "media docena"->6, "un par"->2). Si no se dijo numero, use los valores por defecto de abajo; nunca invente cantidades mayores.
3. Si el audio no contiene ninguna instruccion de venta entendible, devuelva exactamente {"commands": []} y use el "clarify_prompt" del nivel superior.
4. Devuelva EXCLUSIVAMENTE un JSON valido con la forma indicada. Sin texto extra, sin explicaciones, sin markdown.
5. Trate al tendero de USTED en cualquier "clarify_prompt".

ACCIONES PERMITIDAS (campo "action"):
- "agregar": sumar unidades de un producto. Verbos: deme, agregue, eche, anada, otra, una mas, pongame, vendo, vendame, vende, venda, lleva, llevo. IMPORTANTE: el verbo NO es parte del producto; "vendo tres empanadas" -> action="agregar", item="empanada" (nombre del producto solo, en singular si es natural), quantity=3. Por defecto quantity=1 si no se dice numero.
- "quitar": quitar un producto. "quite la gaseosa" (sin numero) = quitar TODO ese producto -> quantity=null. "quite dos aguilas" = restar 2 -> quantity=2.
- "fijar_cantidad": dejar un producto en una cantidad EXACTA (total). Verbos: que sean, dejelo en, ponga ... en total. REQUIERE numero; si no hay numero, NO use esta accion (use "agregar").
- "vaciar": borrar toda la orden actual. "borre todo", "empecemos de nuevo", "cancele la cuenta". item=null.
- "fijar_mesa": dirigir la orden a una mesa. "para la mesa 3", "mesa terraza 2". target.type="mesa", target.mesa = la etiqueta tal cual ("3", "terraza 2"). item=null.
- "fijar_cliente": poner la cuenta a nombre de un cliente. "a nombre de don jose", "para maria". target.type="cliente", target.cliente = el nombre. item=null.
- "cobrar": cerrar y cobrar la cuenta. "cobrele", "a pagar", "listo, cierre". item=null.

DISTINGA agregar de fijar_cantidad: "deme dos" = agregar 2 (suma); "que sean dos"/"dejelo en dos" = fijar_cantidad 2 (total). Ante duda, use "agregar" con confianza menor.

ORDENES COMPUESTAS: una sola frase puede traer varias operaciones. Genere un comando por cada operacion EN EL ORDEN en que se dijeron. Cuando se menciona un objetivo ("para la mesa 3"), emita PRIMERO el comando fijar_mesa/fijar_cliente y luego los comandos de producto que ese objetivo gobierna.

CONFIANZA Y AMBIGUEDAD:
- "confidence" (0.0 a 1.0) por comando: 1.0 = clarisimo; baje si el nombre se oyo borroso, la cantidad es dudosa o no sabe si era agregar o fijar.
- Si un comando es ambiguo pero util, incluyalo con confidence baja y un "clarify_prompt" corto en USTED (ej: "¿Cuantas gaseosas agrego?"). Si todo el audio es ininteligible, use el "clarify_prompt" del nivel superior y deje "commands": [].
- "raw": copie el fragmento exacto del audio que origino cada comando.

FORMA DE SALIDA:
{"commands":[{"action":"...","item":null,"quantity":null,"target":null,"confidence":0.0,"clarify_prompt":null,"raw":""}],"transcript":"","clarify_prompt":null}

EJEMPLO
Audio: "dos aguilas y una agua para la mesa 3, quite la gaseosa"
{"commands":[{"action":"fijar_mesa","item":null,"quantity":null,"target":{"type":"mesa","mesa":"3","cliente":null},"confidence":0.97,"clarify_prompt":null,"raw":"para la mesa 3"},{"action":"agregar","item":"aguila","quantity":2,"target":null,"confidence":0.95,"clarify_prompt":null,"raw":"dos aguilas"},{"action":"agregar","item":"agua","quantity":1,"target":null,"confidence":0.9,"clarify_prompt":null,"raw":"una agua"},{"action":"quitar","item":"gaseosa","quantity":null,"target":null,"confidence":0.93,"clarify_prompt":null,"raw":"quite la gaseosa"}],"transcript":"dos aguilas y una agua para la mesa 3, quite la gaseosa","clarify_prompt":null}

Devuelva el JSON ahora.`

// ExtractVoiceOrder manda el audio a Gemini (temp 0) y devuelve los comandos
// estructurados. Reusa IsSupportedAudioMimeType (voice_inventory.go).
func (s *GeminiService) ExtractVoiceOrder(
	ctx context.Context,
	audioData []byte,
	mimeType string,
) (VoiceOrderResult, error) {
	if s == nil {
		return VoiceOrderResult{}, fmt.Errorf("gemini service not configured")
	}
	if !IsSupportedAudioMimeType(mimeType) {
		return VoiceOrderResult{}, fmt.Errorf("unsupported audio mime type: %s", mimeType)
	}
	raw, err := s.callVoiceOrder(ctx, audioData, mimeType)
	if err != nil {
		return VoiceOrderResult{}, err
	}
	log.Printf("[VOICE_ORDER] gemini raw (%d bytes): %.400s", len(raw), raw)
	res, err := ParseVoiceOrderJSON(raw)
	if err != nil {
		log.Printf("[VOICE_ORDER] parse error: %v | raw=%.300s", err, raw)
		return VoiceOrderResult{}, err
	}
	return res, nil
}

func (s *GeminiService) callVoiceOrder(
	ctx context.Context,
	audioData []byte,
	mimeType string,
) (string, error) {
	b64 := base64.StdEncoding.EncodeToString(audioData)
	payload := map[string]any{
		"contents": []map[string]any{{
			"parts": []map[string]any{
				{"inlineData": map[string]any{"mimeType": mimeType, "data": b64}},
				{"text": VoiceOrderPrompt},
			},
		}},
		"generationConfig": map[string]any{
			"temperature":      0,
			"maxOutputTokens":  1024,
			"responseMimeType": "application/json",
		},
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		s.model, s.apiKey,
	)
	reqCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini voice-order request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read gemini voice-order response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("gemini voice-order returned %d: %.200s", resp.StatusCode, respBody)
	}
	var parsed geminiResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse gemini voice-order envelope: %w", err)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty gemini voice-order response")
	}
	s.recordTokenUsage(ctx, models.AIFeatureVoiceOrder, s.model, &parsed)
	text := parsed.Candidates[0].Content.Parts[0].Text
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	return strings.TrimSpace(text), nil
}

// ParseVoiceOrderJSON decodifica el sobre {commands,transcript,clarify_prompt}.
// Tolerante a fences/texto suelto; descarta comandos con acción inválida; nunca
// lanza por comandos sueltos malformados (sólo si el JSON entero es ilegible).
func ParseVoiceOrderJSON(raw string) (VoiceOrderResult, error) {
	cleaned := stripMarkdownFences(raw)
	if cleaned == "" {
		return VoiceOrderResult{Commands: []VoiceOrderCommand{}}, nil
	}
	// Extraer el objeto entre el primer '{' y el último '}'.
	if open := strings.Index(cleaned, "{"); open >= 0 {
		if end := strings.LastIndex(cleaned, "}"); end > open {
			cleaned = cleaned[open : end+1]
		}
	}
	var res VoiceOrderResult
	if err := json.Unmarshal([]byte(cleaned), &res); err != nil {
		return VoiceOrderResult{}, fmt.Errorf("no se pudo interpretar la respuesta de la IA")
	}
	res.Commands = sanitizeVoiceCommands(res.Commands)
	return res, nil
}

func sanitizeVoiceCommands(in []VoiceOrderCommand) []VoiceOrderCommand {
	out := make([]VoiceOrderCommand, 0, len(in))
	for _, cmd := range in {
		cmd.Action = strings.ToLower(strings.TrimSpace(cmd.Action))
		if _, ok := ValidVoiceActions[cmd.Action]; !ok {
			continue // descarta acciones desconocidas (anti-alucinación)
		}
		if cmd.Item != nil {
			trimmed := strings.TrimSpace(*cmd.Item)
			cmd.Item = &trimmed
		}
		if cmd.Quantity != nil && *cmd.Quantity < 0 {
			zero := 0
			cmd.Quantity = &zero
		}
		if cmd.Confidence < 0 {
			cmd.Confidence = 0
		}
		if cmd.Confidence > 1 {
			cmd.Confidence = 1
		}
		out = append(out, cmd)
	}
	return out
}
