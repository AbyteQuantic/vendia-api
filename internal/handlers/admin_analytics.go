package handlers

import (
	"encoding/json"
	"net/http"
	"time"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// adminOverviewResponse is the exact contract the admin-web
// OverviewPage reads via types/admin.AdminOverview. Naming mismatches
// (e.g. offline_now vs. offline_tenants) caused React crashes in
// production so we lock the shape here and keep a JSON tag per field.
type adminOverviewResponse struct {
	TotalTenants      int64                     `json:"total_tenants"`
	ActiveToday       int64                     `json:"active_today"`
	OfflineNow        int64                     `json:"offline_now"`
	TotalSalesToday   float64                   `json:"total_sales_today"`
	TotalSalesAllTime float64                   `json:"total_sales_all_time"`
	SyncQueuePending  int64                     `json:"sync_queue_pending"`
	TenantsByType     map[string]int64          `json:"tenants_by_type"`
	SalesTrend7d      []adminOverviewSalesPoint `json:"sales_trend_7d"`
}

type adminOverviewSalesPoint struct {
	Date  string  `json:"date"`
	Total float64 `json:"total"`
}

func AdminOverview(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := time.Now()
		startOfDay := now.Truncate(24 * time.Hour)

		var totalTenants int64
		db.Model(&models.Tenant{}).Count(&totalTenants)

		var activeTodayCount int64
		db.Model(&models.Sale{}).
			Where("created_at >= ? AND deleted_at IS NULL", startOfDay).
			Distinct("tenant_id").
			Count(&activeTodayCount)

		var offlineCount int64
		oneHourAgo := now.Add(-1 * time.Hour)
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

		// sync_queue_pending — aggregate of tenants.pending_sync_ops.
		// Already a cached int on every tenant row since migration
		// 010, so a SUM is O(1) per tenant count.
		var syncQueuePending int64
		db.Model(&models.Tenant{}).
			Select("COALESCE(SUM(pending_sync_ops), 0)").
			Scan(&syncQueuePending)

		// tenants_by_type — primary business_type bucket (the first
		// element of the business_types array, kept JSON-encoded in
		// the column since migration 020). Bucketing in Go rather
		// than in SQL keeps the query portable and avoids a fragile
		// substr/json-parse dance that doesn't translate cleanly
		// across dialects.
		var tenantRows []models.Tenant
		db.Model(&models.Tenant{}).
			Select("business_types").
			Where("deleted_at IS NULL").
			Find(&tenantRows)
		tenantsByType := map[string]int64{}
		for _, t := range tenantRows {
			key := "desconocido"
			if len(t.BusinessTypes) > 0 && t.BusinessTypes[0] != "" {
				key = t.BusinessTypes[0]
			}
			tenantsByType[key]++
		}

		// sales_trend_7d — one row per day for the last 7 days,
		// zero-filled for days with no sales so the LineChart
		// renders a continuous axis instead of dropping gaps.
		trendRows := []struct {
			Day   time.Time
			Total float64
		}{}
		db.Model(&models.Sale{}).
			Select(`DATE_TRUNC('day', created_at) AS day,
			        COALESCE(SUM(total), 0) AS total`).
			Where("created_at >= ? AND deleted_at IS NULL",
				startOfDay.Add(-6*24*time.Hour)).
			Group("day").
			Order("day ASC").
			Scan(&trendRows)
		byDay := make(map[string]float64, len(trendRows))
		for _, r := range trendRows {
			byDay[r.Day.Format("2006-01-02")] = r.Total
		}
		trend := make([]adminOverviewSalesPoint, 0, 7)
		for i := 6; i >= 0; i-- {
			d := startOfDay.Add(-time.Duration(i) * 24 * time.Hour).
				Format("2006-01-02")
			trend = append(trend, adminOverviewSalesPoint{
				Date:  d,
				Total: byDay[d],
			})
		}

		c.JSON(http.StatusOK, adminOverviewResponse{
			TotalTenants:      totalTenants,
			ActiveToday:       activeTodayCount,
			OfflineNow:        offlineCount,
			TotalSalesToday:   totalSalesToday,
			TotalSalesAllTime: totalSalesAllTime,
			SyncQueuePending:  syncQueuePending,
			TenantsByType:     tenantsByType,
			SalesTrend7d:      trend,
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

		now := time.Now()

		// Ventas del mes en curso.
		startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		var monthTotal float64
		db.Model(&models.Sale{}).
			Where("tenant_id = ? AND deleted_at IS NULL AND created_at >= ?", tenantID, startOfMonth).
			Select("COALESCE(SUM(total), 0)").Scan(&monthTotal)

		// Serie de ventas de los últimos 30 días (para la gráfica del detalle).
		type dayPoint struct {
			Date  string  `json:"date"`
			Total float64 `json:"total"`
		}
		series := []dayPoint{}
		db.Model(&models.Sale{}).
			Select("TO_CHAR(created_at, 'YYYY-MM-DD') AS date, COALESCE(SUM(total), 0) AS total").
			Where("tenant_id = ? AND deleted_at IS NULL AND created_at >= ?", tenantID, now.AddDate(0, 0, -30)).
			Group("TO_CHAR(created_at, 'YYYY-MM-DD')").
			Order("date ASC").
			Scan(&series)

		// Estado de suscripción canónico (tenant_subscriptions), igual que
		// la lista god-mode.
		sub := loadSubscriptionsByTenantID(db, []string{tenantID})[tenantID]
		subStatus := sub.Status
		if subStatus == "" {
			subStatus = models.SubscriptionStatusFree
		}
		primaryType := ""
		if len(tenant.BusinessTypes) > 0 {
			primaryType = tenant.BusinessTypes[0]
		}

		// La pantalla de detalle (admin-web) lee el tenant en PLANO
		// (AdminTenantDetail), no anidado. Aplanamos el modelo y le sumamos
		// las métricas para que coincida con el contrato y no truene
		// (created_at undefined hacía que formatDate lanzara → página caída).
		resp := gin.H{}
		if b, err := json.Marshal(tenant); err == nil {
			_ = json.Unmarshal(b, &resp)
		}
		resp["business_type"] = primaryType
		resp["subscription_status"] = subStatus
		resp["subscription_ends_at"] = sub.TrialEndsAt
		resp["sales_count"] = salesCount
		resp["sales_total"] = salesTotal
		resp["total_sales_month"] = monthTotal
		resp["sales_30d"] = series

		c.JSON(http.StatusOK, resp)
	}
}

func AdminUpdateSubscription(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Status      string     `json:"status" binding:"required"`
		TrialEndsAt *time.Time `json:"trial_ends_at"`
		EndsAt      *time.Time `json:"ends_at"` // Backward compatibility
	}

	return func(c *gin.Context) {
		tenantID := c.Param("id")

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Explicit status mapping
		statusMap := map[string]string{
			"TRIAL":      models.SubscriptionStatusTrial,
			"PRO_ACTIVE": models.SubscriptionStatusProActive,
			"FREE":       models.SubscriptionStatusFree,
			// Legacy support
			"trial":     models.SubscriptionStatusTrial,
			"active":    models.SubscriptionStatusProActive,
			"suspended": models.SubscriptionStatusProPastDue,
			"cancelled": models.SubscriptionStatusFree,
		}

		newStatus, ok := statusMap[req.Status]
		if !ok {
			// Try direct match if already using new constants
			if _, valid := models.ValidSubscriptionStatuses[req.Status]; valid {
				newStatus = req.Status
			} else {
				c.JSON(http.StatusBadRequest, gin.H{"error": "status inválido"})
				return
			}
		}

		updates := map[string]any{"status": newStatus}

		// Prioritize trial_ends_at over ends_at
		finalEndsAt := req.TrialEndsAt
		if finalEndsAt == nil {
			finalEndsAt = req.EndsAt
		}

		if finalEndsAt != nil && (newStatus == models.SubscriptionStatusTrial || newStatus == models.SubscriptionStatusProActive) {
			updates["trial_ends_at"] = *finalEndsAt
		}

		result := db.Model(&models.TenantSubscription{}).Where("tenant_id = ?", tenantID).Updates(updates)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar suscripción"})
			return
		}
		if result.RowsAffected == 0 {
			// If not found, try to bootstrap it
			sub := models.TenantSubscription{
				TenantID: tenantID,
				Status:   newStatus,
			}
			if finalEndsAt != nil && (newStatus == models.SubscriptionStatusTrial || newStatus == models.SubscriptionStatusProActive) {
				sub.TrialEndsAt = finalEndsAt
			}
			if err := db.Create(&sub).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear suscripción"})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{"message": "suscripción actualizada"})
	}
}
