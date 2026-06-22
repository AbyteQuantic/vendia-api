// Spec: specs/078-centro-tareas-unificado/spec.md
package services

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"vendia-backend/internal/models"
)

// GenerateFunc — firma de generación de texto (GeminiService.GenerateText) para
// poder testear el ranker sin llamar a la IA real.
type GenerateFunc func(ctx context.Context, prompt string) (string, error)

const taskRankTimeout = 800 * time.Millisecond

// RankTasks reordena las tareas con ayuda de la IA SEGÚN CONTEXTO (la hora), sin
// inventar ni borrar: la IA solo devuelve un orden de ids EXISTENTES; cualquier
// fallo/timeout/invalidez → se conserva el orden determinista por reglas. NO
// bloquea (lección Turnstile/Spec 015). Spec 078 F3.
func RankTasks(tasks []models.Task, hour int, generate GenerateFunc) []models.Task {
	if len(tasks) < 2 || generate == nil {
		return tasks
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskRankTimeout)
	defer cancel()

	out, err := generate(ctx, buildRankPrompt(tasks, hour))
	if err != nil {
		return tasks // fallback: orden por reglas
	}
	order := parseRankOrder(out)
	reordered := ApplyTaskOrder(tasks, order)
	if reordered == nil {
		return tasks
	}
	return reordered
}

// TaskIDs devuelve los ids en orden (para cachear el resultado del re-rank).
func TaskIDs(tasks []models.Task) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = t.ID
	}
	return out
}

func buildRankPrompt(tasks []models.Task, hour int) string {
	var b strings.Builder
	b.WriteString("Eres el asistente de un tendero colombiano. Ordena estas tareas pendientes ")
	b.WriteString("por lo que MÁS le conviene atender primero según la hora actual (")
	b.WriteString(itoa(hour))
	b.WriteString("h, 24h). Prioriza lo que cuesta plata o pierde un cliente si se demora ")
	b.WriteString("(pedidos de clientes esperando, mesas por cobrar) sobre lo administrativo ")
	b.WriteString("(reordenar, promos). Devuelve SOLO JSON {\"order\":[\"id1\",\"id2\",...]} con ")
	b.WriteString("TODOS los ids exactamente como vienen, sin inventar ni omitir.\n\nTareas:\n")
	for _, t := range tasks {
		b.WriteString("- id=")
		b.WriteString(t.ID)
		b.WriteString(" | ")
		b.WriteString(t.Urgency)
		b.WriteString(" | ")
		b.WriteString(t.Title)
		b.WriteString("\n")
	}
	return b.String()
}

func parseRankOrder(raw string) []string {
	raw = strings.TrimSpace(raw)
	// tolera ```json ... ``` y texto alrededor: recorta al primer { … último }.
	if i := strings.IndexByte(raw, '{'); i >= 0 {
		if j := strings.LastIndexByte(raw, '}'); j > i {
			raw = raw[i : j+1]
		}
	}
	var parsed struct {
		Order []string `json:"order"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	return parsed.Order
}

// ApplyTaskOrder aplica un orden de ids con WHITELIST: solo ids existentes, en el
// orden dado; cualquier tarea no mencionada se anexa al final en su orden previo.
// Devuelve nil si el orden no cubre nada válido (para caer al fallback). Exportado
// para que el caché del handler aplique un orden ya calculado sin re-llamar a la IA.
func ApplyTaskOrder(tasks []models.Task, order []string) []models.Task {
	if len(order) == 0 {
		return nil
	}
	byID := make(map[string]models.Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	used := make(map[string]bool, len(tasks))
	out := make([]models.Task, 0, len(tasks))
	for _, id := range order {
		if t, ok := byID[id]; ok && !used[id] {
			out = append(out, t)
			used[id] = true
		}
	}
	if len(out) == 0 {
		return nil // la IA no devolvió ningún id válido
	}
	// anexa las que la IA omitió, preservando su orden original.
	for _, t := range tasks {
		if !used[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
