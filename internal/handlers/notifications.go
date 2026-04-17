package handlers

import (
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ListNotifications(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var notifs []models.Notification
		db.Where("tenant_id = ?", tenantID).
			Order("created_at DESC").
			Limit(50).
			Find(&notifs)

		var unread int64
		db.Model(&models.Notification{}).
			Where("tenant_id = ? AND is_read = false", tenantID).
			Count(&unread)

		c.JSON(http.StatusOK, gin.H{
			"data":         notifs,
			"unread_count": unread,
		})
	}
}

func MarkNotificationsRead(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		db.Model(&models.Notification{}).
			Where("tenant_id = ? AND is_read = false", tenantID).
			Update("is_read", true)
		c.JSON(http.StatusOK, gin.H{"message": "marcadas como leídas"})
	}
}

// CreateNotification is a helper, not an endpoint.
func CreateNotification(db *gorm.DB, tenantID, title, body, nType string) {
	notif := models.Notification{
		TenantID: tenantID,
		Title:    title,
		Body:     body,
		Type:     nType,
	}
	db.Create(&notif)
}
