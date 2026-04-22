package handlers

import (
	"net/http"
	"time"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func AdminOverview(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		startOfDay := time.Now().Truncate(24 * time.Hour)

		var totalTenants int64
		db.Model(&models.Tenant{}).Count(&totalTenants)

		var activeTodayCount int64
		db.Model(&models.Sale{}).
			Where("created_at >= ? AND deleted_at IS NULL", startOfDay).
			Distinct("tenant_id").
			Count(&activeTodayCount)

		var offlineCount int64
		oneHourAgo := time.Now().Add(-1 * time.Hour)
		db.Model(&models.Tenant{}).
			Where("last_sync_at IS NOT NULL AND last_sync_at < ?", oneHourAgo).
			Count(&offlineCount)

		var totalSalesToday float64
		db.Model(&models.Sale{}).
			Where("created_at >= ? AND deleted_at IS NULL", startOfDay).
			Select("COALESCE(SUM(total), 0)").
			Scan(&totalSalesToday)

		var totalSalesAllTime float64
		db.Model(&models.Sale{}).
			Where("deleted_at IS NULL").
			Select("COALESCE(SUM(total), 0)").
			Scan(&totalSalesAllTime)

		c.JSON(http.StatusOK, gin.H{
			"total_tenants":       totalTenants,
			"active_today":        activeTodayCount,
			"offline_tenants":     offlineCount,
			"total_sales_today":   totalSalesToday,
			"total_sales_all_time": totalSalesAllTime,
		})
	}
}

// AdminListTenants moved to admin_tenants.go — the Phase 1 god-mode
// endpoint replaces the paginated variant. If pagination is needed
// later it belongs on the new shape, not a parallel handler.

func AdminGetTenant(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := c.Param("id")

		var tenant models.Tenant
		if err := db.Preload("Employees").
			First(&tenant, "id = ?", tenantID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tenant no encontrado"})
			return
		}

		var salesCount int64
		var salesTotal float64
		db.Model(&models.Sale{}).Where("tenant_id = ? AND deleted_at IS NULL", tenantID).Count(&salesCount)
		db.Model(&models.Sale{}).Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
			Select("COALESCE(SUM(total), 0)").Scan(&salesTotal)

		c.JSON(http.StatusOK, gin.H{
			"tenant":      tenant,
			"sales_count": salesCount,
			"sales_total": salesTotal,
		})
	}
}

func AdminUpdateSubscription(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Status string     `json:"status" binding:"required"`
		EndsAt *time.Time `json:"ends_at"`
	}

	return func(c *gin.Context) {
		tenantID := c.Param("id")

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		valid := map[string]bool{"trial": true, "active": true, "suspended": true, "cancelled": true}
		if !valid[req.Status] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "status inválido"})
			return
		}

		updates := map[string]any{"subscription_status": req.Status}
		if req.EndsAt != nil {
			updates["subscription_ends_at"] = *req.EndsAt
		}

		result := db.Model(&models.Tenant{}).Where("id = ?", tenantID).Updates(updates)
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "tenant no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "suscripción actualizada"})
	}
}
