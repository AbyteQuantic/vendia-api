// Spec: specs/067-planear-menu-ia-ux/spec.md
package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetMenuPlanToday — GET /api/v1/menu-plan/today[?branch=<id>]. Devuelve el menú
// EFECTIVO que el link público muestra HOY, con la MISMA resolución que el
// catálogo (override > plantilla; lookahead de 7 días; fallback de sede al plan
// del comercio). Read-only: alimenta el preview "así se ve hoy su menú en línea"
// dentro de la app. No persiste nada.
func GetMenuPlanToday(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		branchID := c.Query("branch")

		var plan models.WeeklyMenuPlan
		effectiveBranch := branchID
		err := db.Where("tenant_id = ? AND branch_id = ?", tenantID, branchID).First(&plan).Error
		if err != nil && branchID != "" {
			// La sede sin plan propio hereda el plan por defecto del comercio.
			err = db.Where("tenant_id = ? AND branch_id = ?", tenantID, "").First(&plan).Error
			effectiveBranch = ""
		}
		if err != nil {
			// Sin plan guardado: el link se comporta como antes (legacy, Art. X).
			c.JSON(http.StatusOK, gin.H{"data": gin.H{
				"active": false, "found": false, "items": []gin.H{},
			}})
			return
		}

		days := map[string]services.DayPlan{}
		if strings.TrimSpace(plan.Days) != "" {
			_ = json.Unmarshal([]byte(plan.Days), &days)
		}

		today := time.Now().In(services.LoadTimezone())
		todayKey := today.Format("2006-01-02")
		until := today.AddDate(0, 0, 6).Format("2006-01-02")

		var ovRows []models.MenuPlanOverride
		db.Where("tenant_id = ? AND branch_id = ? AND date >= ? AND date <= ?",
			tenantID, effectiveBranch, todayKey, until).Find(&ovRows)
		overrides := make(map[string]services.DayPlan, len(ovRows))
		for _, rr := range ovRows {
			items := []services.MenuPlanItem{}
			if strings.TrimSpace(rr.Items) != "" {
				_ = json.Unmarshal([]byte(rr.Items), &items)
			}
			overrides[rr.Date] = services.DayPlan{Enabled: rr.Enabled, Items: items}
		}

		eff := services.ResolveEffectiveMenu(days, overrides, today)
		out := gin.H{
			"active":    true,
			"found":     eff.Found,
			"is_today":  eff.IsToday,
			"day_label": services.MenuDayLabel(eff),
			"weekday":   services.DayLabelES(eff.DayKey),
			"items":     []gin.H{},
		}
		if !eff.Found {
			c.JSON(http.StatusOK, gin.H{"data": out})
			return
		}

		// Nombres de los platos del día resuelto.
		ids := make([]string, 0, len(eff.Items))
		for _, it := range eff.Items {
			ids = append(ids, it.RecipeUUID)
		}
		var recipes []models.Recipe
		db.Where("tenant_id = ? AND id IN ?", tenantID, ids).Find(&recipes)
		nameByID := make(map[string]string, len(recipes))
		for _, rc := range recipes {
			nameByID[rc.ID] = rc.ProductName
		}

		items := make([]gin.H, 0, len(eff.Items))
		for _, it := range eff.Items {
			name := nameByID[it.RecipeUUID]
			if strings.TrimSpace(name) == "" {
				name = "Plato"
			}
			items = append(items, gin.H{
				"recipe_uuid": it.RecipeUUID,
				"name":        name,
				"planned_qty": it.PlannedQty,
			})
		}
		out["items"] = items
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}
