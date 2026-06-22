// Spec: specs/078-centro-tareas-unificado/spec.md
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

// weekdayOrder — orden lun→dom para mostrar las etiquetas de día estables.
var weekdayOrder = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// recipeInMenus escanea los planes semanales y los overrides (hoy..+6) del tenant
// y devuelve: las claves de día donde aparece la receta, las fechas de override,
// y si está en el menú EFECTIVO de hoy de alguna sede. Spec 078.
func recipeInMenus(db *gorm.DB, tenantID, recipeID string) (dayKeys map[string]bool, dates []string, activeToday bool) {
	dayKeys = map[string]bool{}
	today := time.Now().In(services.LoadTimezone())
	todayKey := today.Format("2006-01-02")
	until := today.AddDate(0, 0, 6).Format("2006-01-02")

	var ovRows []models.MenuPlanOverride
	db.Where("tenant_id = ? AND date >= ? AND date <= ?", tenantID, todayKey, until).Find(&ovRows)
	ovByBranch := map[string]map[string]services.DayPlan{}
	for _, rr := range ovRows {
		var items []services.MenuPlanItem
		if strings.TrimSpace(rr.Items) != "" {
			_ = json.Unmarshal([]byte(rr.Items), &items)
		}
		if ovByBranch[rr.BranchID] == nil {
			ovByBranch[rr.BranchID] = map[string]services.DayPlan{}
		}
		ovByBranch[rr.BranchID][rr.Date] = services.DayPlan{Enabled: rr.Enabled, Items: items}
		for _, it := range items {
			if strings.TrimSpace(it.RecipeUUID) == recipeID {
				dates = append(dates, rr.Date)
				break
			}
		}
	}

	var plans []models.WeeklyMenuPlan
	db.Where("tenant_id = ?", tenantID).Find(&plans)
	for _, plan := range plans {
		var days map[string]services.DayPlan
		if strings.TrimSpace(plan.Days) != "" {
			_ = json.Unmarshal([]byte(plan.Days), &days)
		}
		for k, dp := range days {
			for _, it := range dp.Items {
				if strings.TrimSpace(it.RecipeUUID) == recipeID {
					dayKeys[k] = true
					break
				}
			}
		}
		eff := services.ResolveEffectiveMenu(days, ovByBranch[plan.BranchID], today)
		if _, ok := services.RecipeUUIDSet(eff)[recipeID]; ok {
			activeToday = true
		}
	}
	return
}

// RecipeMenuUsage — GET /api/v1/recipes/:uuid/menu-usage. Dice si la receta está
// en algún menú (y en cuáles días/fechas) y si está activa en el menú de HOY, para
// que la app decida si bloquear o pedir confirmación de eliminación. Spec 078.
func RecipeMenuUsage(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")
		var recipe models.Recipe
		if err := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).First(&recipe).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "receta no encontrada"})
			return
		}
		dayKeys, dates, activeToday := recipeInMenus(db, tenantID, uuid)
		labels := make([]string, 0, len(dayKeys))
		for _, k := range weekdayOrder {
			if dayKeys[k] {
				labels = append(labels, services.DayLabelES(k))
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"active_today": activeToday,
			"in_menu":      len(labels) > 0 || len(dates) > 0,
			"day_labels":   labels,
			"dates":        dates,
			"summary":      strings.Join(labels, ", "),
		}})
	}
}

// stripRecipeFromMenus quita recipeID de todos los planes semanales y overrides
// del tenant (idempotente). Mantiene el estado coherente al eliminar una receta
// (Art. IX): el menú no debe quedar con un recipe_uuid colgado. Spec 078.
func stripRecipeFromMenus(tx *gorm.DB, tenantID, recipeID string) error {
	var plans []models.WeeklyMenuPlan
	if err := tx.Where("tenant_id = ?", tenantID).Find(&plans).Error; err != nil {
		return err
	}
	for _, plan := range plans {
		if strings.TrimSpace(plan.Days) == "" {
			continue
		}
		var days map[string]services.DayPlan
		if err := json.Unmarshal([]byte(plan.Days), &days); err != nil {
			continue
		}
		changed := false
		for k, dp := range days {
			kept := make([]services.MenuPlanItem, 0, len(dp.Items))
			for _, it := range dp.Items {
				if strings.TrimSpace(it.RecipeUUID) == recipeID {
					changed = true
					continue
				}
				kept = append(kept, it)
			}
			dp.Items = kept
			days[k] = dp
		}
		if changed {
			b, _ := json.Marshal(days)
			if err := tx.Model(&models.WeeklyMenuPlan{}).Where("id = ?", plan.ID).
				Update("days", string(b)).Error; err != nil {
				return err
			}
		}
	}
	var ovs []models.MenuPlanOverride
	if err := tx.Where("tenant_id = ?", tenantID).Find(&ovs).Error; err != nil {
		return err
	}
	for _, ov := range ovs {
		if strings.TrimSpace(ov.Items) == "" {
			continue
		}
		var items []services.MenuPlanItem
		if err := json.Unmarshal([]byte(ov.Items), &items); err != nil {
			continue
		}
		kept := make([]services.MenuPlanItem, 0, len(items))
		changed := false
		for _, it := range items {
			if strings.TrimSpace(it.RecipeUUID) == recipeID {
				changed = true
				continue
			}
			kept = append(kept, it)
		}
		if changed {
			b, _ := json.Marshal(kept)
			if err := tx.Model(&models.MenuPlanOverride{}).Where("id = ?", ov.ID).
				Update("items", string(b)).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
