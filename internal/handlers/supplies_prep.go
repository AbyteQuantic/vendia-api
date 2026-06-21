// Spec: specs/076-alistar-insumos-del-dia/spec.md
package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// weekdayKey mapea time.Weekday (Sunday=0) a la clave del JSONB del menú.
var prepWeekdayKeys = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// parseLeadingInt — número líder de un texto ("10 porciones" → 10, "" → def).
func parseLeadingInt(s string, def int) int {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return def
	}
	if n, err := strconv.Atoi(s[:i]); err == nil && n > 0 {
		return n
	}
	return def
}

type PrepIngredient struct {
	IngredientID  string  `json:"ingredient_id"`
	Name          string  `json:"name"`
	Unit          string  `json:"unit"`
	QtyPerPortion float64 `json:"qty_per_portion"`
}

type PrepDish struct {
	RecipeUUID      string           `json:"recipe_uuid"`
	Name            string           `json:"name"`
	DefaultPortions int              `json:"default_portions"`
	Ingredients     []PrepIngredient `json:"ingredients"`
}

// SuppliesPrepList — GET /api/v1/supplies/prep-list?date=YYYY-MM-DD[&branch=...]
// Alista los insumos del día (Spec 076): resuelve el menú EXACTO de la fecha y
// explota cada plato a sus insumos por-porción. Read-only.
func SuppliesPrepList(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		branchID := c.Query("branch")

		dateStr := c.Query("date")
		var day time.Time
		if t, err := time.Parse("2006-01-02", dateStr); err == nil {
			day = t
		} else {
			day = time.Now().In(services.LoadTimezone())
			dateStr = day.Format("2006-01-02")
		}
		dayKey := prepWeekdayKeys[int(day.Weekday())]

		items := resolveMenuItemsForDate(db, tenantID, branchID, dateStr, dayKey)

		dishes := make([]PrepDish, 0, len(items))
		for _, it := range items {
			rid := strings.TrimSpace(it.RecipeUUID)
			if rid == "" {
				continue
			}
			var recipe models.Recipe
			if err := db.Preload("Ingredients").
				Where("tenant_id = ? AND id = ?", tenantID, rid).First(&recipe).Error; err != nil {
				continue
			}
			yield := parseLeadingInt(recipe.Yield, 1)
			if yield < 1 {
				yield = 1
			}
			// Unidades de los insumos referenciados (una sola query).
			ingIDs := make([]string, 0, len(recipe.Ingredients))
			for _, ri := range recipe.Ingredients {
				if ri.IngredientID != nil && *ri.IngredientID != "" {
					ingIDs = append(ingIDs, *ri.IngredientID)
				}
			}
			unitByID := map[string]models.Ingredient{}
			if len(ingIDs) > 0 {
				var ings []models.Ingredient
				db.Where("tenant_id = ? AND id IN ?", tenantID, ingIDs).Find(&ings)
				for _, g := range ings {
					unitByID[g.ID] = g
				}
			}
			lines := make([]PrepIngredient, 0, len(recipe.Ingredients))
			for _, ri := range recipe.Ingredients {
				if ri.IngredientID == nil || *ri.IngredientID == "" {
					continue
				}
				g := unitByID[*ri.IngredientID]
				name := ri.ProductName
				if strings.TrimSpace(name) == "" {
					name = g.Name
				}
				lines = append(lines, PrepIngredient{
					IngredientID:  *ri.IngredientID,
					Name:          name,
					Unit:          g.Unit,
					QtyPerPortion: ri.Quantity / float64(yield),
				})
			}
			portions := it.PlannedQty
			if portions <= 0 {
				portions = 10
			}
			dishes = append(dishes, PrepDish{
				RecipeUUID:      rid,
				Name:            recipe.ProductName,
				DefaultPortions: portions,
				Ingredients:     lines,
			})
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"date":    dateStr,
			"weekday": services.DayLabelES(dayKey),
			"dishes":  dishes,
		}})
	}
}

// resolveMenuItemsForDate — menú EXACTO de la fecha: override de la fecha (si
// existe, manda enabled/disabled); si no, plantilla del día de semana; fallback
// de sede al plan del comercio. No hace lookahead (queremos ese día puntual).
func resolveMenuItemsForDate(db *gorm.DB, tenantID, branchID, dateStr, dayKey string) []services.MenuPlanItem {
	// 1) override de la fecha exacta (en la sede o, si no, en el comercio).
	effectiveBranch := branchID
	var ov models.MenuPlanOverride
	err := db.Where("tenant_id = ? AND branch_id = ? AND date = ?", tenantID, branchID, dateStr).First(&ov).Error
	if err != nil && branchID != "" {
		if e2 := db.Where("tenant_id = ? AND branch_id = ? AND date = ?", tenantID, "", dateStr).First(&ov).Error; e2 == nil {
			err = nil
			effectiveBranch = ""
		}
	}
	if err == nil {
		if !ov.Enabled {
			return nil // override apaga el menú ese día
		}
		var items []services.MenuPlanItem
		if strings.TrimSpace(ov.Items) != "" {
			_ = json.Unmarshal([]byte(ov.Items), &items)
		}
		return items
	}

	// 2) plantilla del día de semana.
	var plan models.WeeklyMenuPlan
	perr := db.Where("tenant_id = ? AND branch_id = ?", tenantID, effectiveBranch).First(&plan).Error
	if perr != nil && branchID != "" {
		perr = db.Where("tenant_id = ? AND branch_id = ?", tenantID, "").First(&plan).Error
	}
	if perr != nil {
		return nil
	}
	days := map[string]services.DayPlan{}
	if strings.TrimSpace(plan.Days) != "" {
		_ = json.Unmarshal([]byte(plan.Days), &days)
	}
	dp, ok := days[dayKey]
	if !ok || !dp.Enabled {
		return nil
	}
	return dp.Items
}
