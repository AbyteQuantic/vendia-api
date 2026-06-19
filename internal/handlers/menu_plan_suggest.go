// Spec: specs/067-planear-menu-ia-ux/spec.md
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/aiusage"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// minRecipesForSuggest — umbral mínimo de recetas para que valga la pena pedir
// una propuesta a la IA. Por debajo, el corte temprano evita una llamada inútil.
const minRecipesForSuggest = 3

// SuggestMenuPlan — POST /api/v1/menu-plan/suggest[?branch=<id>]. Propone una
// plantilla semanal con IA usando ÚNICAMENTE las recetas reales del tenant.
//
// STATELESS: no persiste nada. El cliente revisa/edita la propuesta y luego
// hace PUT /menu-plan (el "Guardar" del header). La IA nunca auto-guarda.
//
// Anti-alucinación: el guard REAL es la whitelist server-side. Se re-derivan las
// recetas del tenant desde la DB (no se confía en el cliente) y parseSuggestedDays
// descarta cualquier recipe_uuid fuera de ese set, sin confiar en la temperatura.
func SuggestMenuPlan(db *gorm.DB, geminiSvc *services.GeminiService) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de IA no configurado"})
			return
		}

		// Re-deriva las recetas del tenant desde la DB. La whitelist y el catálogo
		// numerado para el prompt salen de aquí — nunca del input del cliente.
		var recipes []models.Recipe
		db.Where("tenant_id = ?", tenantID).Order("product_name ASC").Find(&recipes)

		if len(recipes) < minRecipesForSuggest {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "Primero cree algunas recetas; con al menos 3 podemos sugerir su menú de la semana.",
			})
			return
		}

		allowed := make(map[string]struct{}, len(recipes))
		var catalog strings.Builder
		for _, r := range recipes {
			allowed[r.ID] = struct{}{}
			cat := strings.TrimSpace(r.Category)
			if cat == "" {
				cat = "general"
			}
			catalog.WriteString(fmt.Sprintf("- %s | %s | categoría: %s\n", r.ID, r.ProductName, cat))
		}

		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), tenantID), 45*time.Second)
		defer cancel()

		text, err := geminiSvc.GenerateText(ctx, buildMenuSuggestPrompt(catalog.String()))
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "No pudimos sugerir su menú. Arme su semana manualmente o intente de nuevo.",
			})
			return
		}

		days := parseSuggestedDays(text, allowed)
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"days": days, "branch_id": c.Query("branch")}})
	}
}

// buildMenuSuggestPrompt arma el prompt con la lista CERRADA de recetas. Pide a
// Gemini distribuir un menú semanal usando solo esos uuids, en español USTED.
func buildMenuSuggestPrompt(catalog string) string {
	return fmt.Sprintf(`Eres el ayudante de un restaurante colombiano que vende almuerzos. Arma una propuesta de menú para la semana (de lunes a domingo).

CATÁLOGO DE RECETAS DISPONIBLES (formato: <recipe_uuid> | <nombre> | <categoría>):
%s
REGLAS ESTRICTAS:
- Usa ÚNICAMENTE los recipe_uuid de la lista anterior. PROHIBIDO inventar, renombrar o crear uuids nuevos.
- Varía los platos por categoría; evita repetir el mismo plato en días seguidos.
- Propón un menú razonable por día (2 a 4 platos). Deja "planned_qty": 0 (es solo guía, no inventario).
- Si para un día no hay platos apropiados, deja ese día con "enabled": false e "items": [].
- Es válido sugerir cerrar el domingo si el surtido es pequeño.

Devuelve SOLO un JSON con esta forma EXACTA, sin texto adicional ni explicaciones:
{"days":{"mon":{"enabled":true,"items":[{"recipe_uuid":"<uuid>","planned_qty":0}]},"tue":{"enabled":true,"items":[]},"wed":{"enabled":true,"items":[]},"thu":{"enabled":true,"items":[]},"fri":{"enabled":true,"items":[]},"sat":{"enabled":true,"items":[]},"sun":{"enabled":false,"items":[]}}}`, catalog)
}

// parseSuggestedDays interpreta la respuesta de Gemini y la SANEA contra la
// whitelist del tenant. Es el guard real anti-alucinación: descarta días
// inválidos y recipe_uuid fuera del set permitido; un día sin ítems válidos
// queda apagado. Tolera cercas markdown y texto suelto alrededor del JSON.
func parseSuggestedDays(text string, allowed map[string]struct{}) map[string]services.DayPlan {
	clean := map[string]services.DayPlan{}

	var parsed struct {
		Days map[string]services.DayPlan `json:"days"`
	}
	if err := json.Unmarshal([]byte(stripJSONFence(text)), &parsed); err != nil {
		return clean // peor caso: días vacíos; el cliente arma a mano.
	}

	for k, dp := range parsed.Days {
		if _, ok := validWeekdayKeys[k]; !ok {
			continue
		}
		items := make([]services.MenuPlanItem, 0, len(dp.Items))
		for _, it := range dp.Items {
			u := strings.TrimSpace(it.RecipeUUID)
			if u == "" {
				continue
			}
			if _, ok := allowed[u]; !ok {
				continue // uuid inventado → fuera.
			}
			qty := it.PlannedQty
			if qty < 0 {
				qty = 0
			}
			items = append(items, services.MenuPlanItem{RecipeUUID: u, PlannedQty: qty})
		}
		clean[k] = services.DayPlan{Enabled: dp.Enabled && len(items) > 0, Items: items}
	}
	return clean
}

// stripJSONFence quita cercas markdown (```json … ```) y texto suelto antes del
// primer '{' o después del último '}'. Mismo criterio que stripMarkdownJSON del
// servicio Gemini, replicado aquí para no exportar interno del paquete services.
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```JSON")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if i := strings.LastIndex(s, "}"); i >= 0 && i < len(s)-1 {
		s = s[:i+1]
	}
	return s
}
