// Spec: specs/038-push-notifications-web-android/spec.md
package handlers

import (
	"errors"
	"net/http"
	"strings"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services/push"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type broadcastPushRequest struct {
	TenantID string `json:"tenant_id"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	DeepLink string `json:"deep_link,omitempty"`
}

// BroadcastPush es POST /api/v1/admin/push/broadcast. Protegido por
// `middleware.SuperAdminOnly` — solo el operador VendIA puede usarlo.
// Delega toda la lógica de elegibilidad / cap / envío al dispatcher
// para compartir reglas con los 4 triggers automáticos. Cada broadcast
// es un evento único (genera un dedup_key UUID propio) para que el
// mismo super_admin pueda mandar el mismo título múltiples veces si
// hace falta (la dedup es contra reintentos del sender, no contra la
// intención del operador).
//
// Spec: specs/038-push-notifications-web-android/spec.md — FR-10, AC-09, AC-16.
func BroadcastPush(db *gorm.DB, dispatcher *push.Dispatcher) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req broadcastPushRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "body inválido"})
			return
		}
		req.TenantID = strings.TrimSpace(req.TenantID)
		req.Title = strings.TrimSpace(req.Title)
		if req.TenantID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id requerido"})
			return
		}
		if req.Title == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "title requerido"})
			return
		}

		// Existencia del tenant — 404 si no existe (Art. VI: no
		// distinguir "no existe" de "no autorizado" para el caller
		// externo, pero el super_admin puede tener feedback claro).
		var tenant models.Tenant
		err := db.Where("id = ?", req.TenantID).First(&tenant).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "tenant no encontrado"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo consultar el tenant"})
			return
		}

		outcome, err := dispatcher.DispatchEvent(c.Request.Context(), db, push.Event{
			TenantID: req.TenantID,
			Type:     "admin_manual",
			Title:    req.Title,
			Body:     req.Body,
			DeepLink: req.DeepLink,
			// Cada broadcast es un evento único — un UUID nuevo evita
			// que se confunda con un reintento.
			DedupKey: "admin-broadcast:" + uuid.NewString(),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo despachar la push"})
			return
		}

		c.JSON(http.StatusAccepted, gin.H{"data": gin.H{
			"in_app_notification_id": outcome.NotificationID,
			"tokens_targeted":        outcome.TokensSent,
			"tokens_invalidated":     outcome.TokensInvalid,
			"status":                 outcome.Status,
		}})
	}
}
