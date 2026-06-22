// Spec: specs/078-centro-tareas-unificado/spec.md
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const maxCategoryLen = 40

// SuggestProductCategories — POST /api/v1/products/suggest-categories. Para los
// productos SIN categoría, Gemini infiere una categoría corta desde el nombre
// (reusando las categorías que el tenant ya tiene). NO aplica nada: devuelve
// sugerencias [{id, name, suggested}] para que el tenant revise/edite y confirme
// en la app. Anti-alucinación: solo se aceptan ids del lote. Spec 078.
func SuggestProductCategories(db *gorm.DB, geminiSvc *services.GeminiService) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "IA no disponible"})
			return
		}

		var products []models.Product
		db.Where("tenant_id = ? AND deleted_at IS NULL AND COALESCE(TRIM(category), '') = ''", tenantID).
			Order("name ASC").Limit(200).Find(&products)
		if len(products) == 0 {
			c.JSON(http.StatusOK, gin.H{"data": []gin.H{}})
			return
		}

		var existing []string
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND category <> ''", tenantID).
			Distinct().Limit(50).Pluck("category", &existing)

		ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
		defer cancel()
		text, err := geminiSvc.GenerateText(ctx, buildCategoryPrompt(products, existing))
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "no se pudo sugerir categorías"})
			return
		}

		nameByID := make(map[string]string, len(products))
		for _, p := range products {
			nameByID[p.ID] = p.Name
		}
		c.JSON(http.StatusOK, gin.H{"data": parseCategorySuggestions(text, nameByID)})
	}
}

func buildCategoryPrompt(products []models.Product, existing []string) string {
	var b strings.Builder
	for _, p := range products {
		b.WriteString(p.ID)
		b.WriteString(" | ")
		b.WriteString(p.Name)
		b.WriteString("\n")
	}
	existingHint := "ninguna aún"
	if len(existing) > 0 {
		existingHint = strings.Join(existing, ", ")
	}
	return fmt.Sprintf(`Eres un asistente que ORGANIZA un catálogo de tienda en categorías.
Para cada producto, asigna UNA categoría corta (1 a 3 palabras), en español, Title Case.

REGLAS:
- REUSA estas categorías existentes cuando encajen: %s
- Si ninguna encaja, crea una categoría general y consistente (agrupa productos similares bajo el MISMO nombre).
- Categoría corta y comercial (ej. "Lubricantes", "Perfumes", "Suplementos", "Juguetes", "Bebidas").
- NO inventes ids. Usa EXACTAMENTE los id de la lista.

PRODUCTOS (formato: <id> | <nombre>):
%s

Responde SOLO este JSON, sin texto extra:
{"items":[{"id":"<id>","category":"<categoria>"}]}`, existingHint, b.String())
}

// parseCategorySuggestions valida la salida de Gemini: solo ids del lote,
// categoría no vacía y de largo razonable. Devuelve [{id, name, suggested}].
func parseCategorySuggestions(text string, nameByID map[string]string) []gin.H {
	var parsed struct {
		Items []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
		} `json:"items"`
	}
	out := make([]gin.H, 0, len(nameByID))
	if err := json.Unmarshal([]byte(stripJSONFence(text)), &parsed); err != nil {
		return out
	}
	seen := map[string]bool{}
	for _, it := range parsed.Items {
		id := strings.TrimSpace(it.ID)
		name, ok := nameByID[id]
		if !ok || seen[id] {
			continue // id inventado o duplicado → descartar
		}
		cat := strings.TrimSpace(it.Category)
		if cat == "" {
			continue
		}
		if len(cat) > maxCategoryLen {
			cat = strings.TrimSpace(cat[:maxCategoryLen])
		}
		seen[id] = true
		out = append(out, gin.H{"id": id, "name": name, "suggested": cat})
	}
	return out
}

// BulkUpdateCategories — POST /api/v1/products/categories/bulk. Aplica las
// categorías que el tenant confirmó/editó: body {items:[{id, category}]}.
// Tenant-scoped, idempotente. Spec 078.
func BulkUpdateCategories(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var req struct {
			Items []struct {
				ID       string `json:"id"`
				Category string `json:"category"`
			} `json:"items"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "datos inválidos"})
			return
		}
		updated := 0
		err := db.Transaction(func(tx *gorm.DB) error {
			for _, it := range req.Items {
				id := strings.TrimSpace(it.ID)
				if id == "" {
					continue
				}
				cat := strings.TrimSpace(it.Category)
				if len(cat) > maxCategoryLen {
					cat = strings.TrimSpace(cat[:maxCategoryLen])
				}
				res := tx.Model(&models.Product{}).
					Where("id = ? AND tenant_id = ?", id, tenantID).
					Update("category", cat)
				if res.Error != nil {
					return res.Error
				}
				updated += int(res.RowsAffected)
			}
			return nil
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudieron guardar las categorías"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"updated": updated}})
	}
}
