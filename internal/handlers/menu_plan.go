// Spec: specs/066-planear-menu/spec.md
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

// validWeekdayKeys es el conjunto de claves de día aceptadas en la plantilla.
var validWeekdayKeys = map[string]struct{}{
	"mon": {}, "tue": {}, "wed": {}, "thu": {}, "fri": {}, "sat": {}, "sun": {},
}

// menuPlanRequest es el cuerpo del PUT de la plantilla semanal: un mapa de
// día → plan. Reusa los DTO del servicio para mantener un solo contrato.
type menuPlanRequest struct {
	Days map[string]services.DayPlan `json:"days"`
}

// GetMenuPlan — GET /api/v1/menu-plan. Devuelve la plantilla semanal del
// comercio (AC-07). Si no existe aún, devuelve un plan vacío (no es error):
// el front lo trata como "sin planear" y muestra los 7 días apagados.
func GetMenuPlan(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var plan models.WeeklyMenuPlan
		err := db.Where("tenant_id = ?", tenantID).First(&plan).Error
		days := map[string]services.DayPlan{}
		if err == nil && strings.TrimSpace(plan.Days) != "" {
			_ = json.Unmarshal([]byte(plan.Days), &days)
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"days": days}})
	}
}

// UpsertMenuPlan — PUT /api/v1/menu-plan. Reemplaza la plantilla semanal del
// comercio (AC-04/AC-07). Inhabilitar un día NO borra sus platos: el cuerpo
// completo es la nueva verdad y el front conserva los ítems al apagar.
func UpsertMenuPlan(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req menuPlanRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo inválido"})
			return
		}
		// Sanea: solo claves de día válidas y planned_qty no negativo.
		clean := map[string]services.DayPlan{}
		for k, dp := range req.Days {
			if _, ok := validWeekdayKeys[k]; !ok {
				continue
			}
			items := make([]services.MenuPlanItem, 0, len(dp.Items))
			for _, it := range dp.Items {
				if strings.TrimSpace(it.RecipeUUID) == "" {
					continue
				}
				qty := it.PlannedQty
				if qty < 0 {
					qty = 0
				}
				items = append(items, services.MenuPlanItem{RecipeUUID: it.RecipeUUID, PlannedQty: qty})
			}
			clean[k] = services.DayPlan{Enabled: dp.Enabled, Items: items}
		}

		raw, _ := json.Marshal(clean)
		var plan models.WeeklyMenuPlan
		err := db.Where("tenant_id = ?", tenantID).First(&plan).Error
		if err == gorm.ErrRecordNotFound {
			plan = models.WeeklyMenuPlan{TenantID: tenantID, Days: string(raw)}
			if err := db.Create(&plan).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar el menú"})
				return
			}
		} else {
			plan.Days = string(raw)
			if err := db.Save(&plan).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar el menú"})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"days": clean}})
	}
}

// overrideRequest es el cuerpo del PUT de un ajuste por fecha.
type overrideRequest struct {
	Date    string                  `json:"date"`
	Enabled bool                    `json:"enabled"`
	Items   []services.MenuPlanItem `json:"items"`
}

// ListMenuPlanOverrides — GET /api/v1/menu-plan/overrides. Lista los ajustes
// por fecha del comercio de hoy en adelante (AC-05).
func ListMenuPlanOverrides(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		today := time.Now().In(services.LoadTimezone()).Format("2006-01-02")

		var rows []models.MenuPlanOverride
		db.Where("tenant_id = ? AND date >= ?", tenantID, today).
			Order("date ASC").Find(&rows)

		type overrideOut struct {
			Date    string                  `json:"date"`
			Enabled bool                    `json:"enabled"`
			Items   []services.MenuPlanItem `json:"items"`
		}
		out := make([]overrideOut, 0, len(rows))
		for _, r := range rows {
			items := []services.MenuPlanItem{}
			if strings.TrimSpace(r.Items) != "" {
				_ = json.Unmarshal([]byte(r.Items), &items)
			}
			out = append(out, overrideOut{Date: r.Date, Enabled: r.Enabled, Items: items})
		}
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}

// UpsertMenuPlanOverride — PUT /api/v1/menu-plan/overrides. Crea o reemplaza el
// ajuste de una fecha concreta (AC-05). La fecha debe ser YYYY-MM-DD válida.
func UpsertMenuPlanOverride(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req overrideRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo inválido"})
			return
		}
		if _, err := time.Parse("2006-01-02", req.Date); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "fecha inválida (use AAAA-MM-DD)"})
			return
		}
		items := make([]services.MenuPlanItem, 0, len(req.Items))
		for _, it := range req.Items {
			if strings.TrimSpace(it.RecipeUUID) == "" {
				continue
			}
			qty := it.PlannedQty
			if qty < 0 {
				qty = 0
			}
			items = append(items, services.MenuPlanItem{RecipeUUID: it.RecipeUUID, PlannedQty: qty})
		}
		raw, _ := json.Marshal(items)

		var ov models.MenuPlanOverride
		err := db.Where("tenant_id = ? AND date = ?", tenantID, req.Date).First(&ov).Error
		if err == gorm.ErrRecordNotFound {
			ov = models.MenuPlanOverride{TenantID: tenantID, Date: req.Date, Enabled: req.Enabled, Items: string(raw)}
			if err := db.Create(&ov).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar el ajuste"})
				return
			}
		} else {
			ov.Enabled = req.Enabled
			ov.Items = string(raw)
			if err := db.Save(&ov).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar el ajuste"})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"date": ov.Date, "enabled": ov.Enabled, "items": items}})
	}
}

// DeleteMenuPlanOverride — DELETE /api/v1/menu-plan/overrides/:date. Borra el
// ajuste de una fecha; vuelve a regir la plantilla de ese día (AC-05).
func DeleteMenuPlanOverride(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		date := c.Param("date")

		db.Where("tenant_id = ? AND date = ?", tenantID, date).
			Delete(&models.MenuPlanOverride{})
		c.JSON(http.StatusOK, gin.H{"message": "ajuste eliminado"})
	}
}
