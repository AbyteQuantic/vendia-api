// Spec: specs/F38-notifications-deeplink/spec.md
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

// CreateNotification persists a notification with no deep-link
// payload. Kept as the simple call site for legacy emitters
// (`info`/`system`) that have nothing to link to.
//
// New emitters with a routable entity (mesa, abono, pedido,
// fiado) should call [CreateNotificationWithData] so the client
// can deep-link the tile to its action screen/modal.
func CreateNotification(db *gorm.DB, tenantID, title, body, nType string) {
	CreateNotificationWithData(db, tenantID, title, body, nType, nil)
}

// CreateNotificationWithData persists a notification along with a
// JSON payload the client uses to deep-link the tile.
//
// Conventions for `data` keys (stable contract — DO NOT rename
// silently, the Flutter client reads them by name):
//
//   - "order_id"     uuid del KDS order ticket / mesa abierta
//   - "payment_id"   uuid del partial_payment pendiente
//   - "fiado_id"     uuid de la credit_account
//   - "table_label"  string corto ("Mesa 4", "Barra 1")
//
// `data` may be nil — stored as `{}` thanks to the model's
// BeforeCreate. Missing keys are valid; the client falls back to
// the kind's list screen.
func CreateNotificationWithData(
	db *gorm.DB,
	tenantID, title, body, nType string,
	data models.NotificationData,
) {
	if data == nil {
		data = models.NotificationData{}
	}
	notif := models.Notification{
		TenantID: tenantID,
		Title:    title,
		Body:     body,
		Type:     nType,
		Data:     data,
	}
	db.Create(&notif)
}
