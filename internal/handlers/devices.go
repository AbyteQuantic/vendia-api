// Spec: specs/038-push-notifications-web-android/spec.md
package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerDeviceRequest es el contrato del POST /devices/register.
// El tenant y el user vienen del JWT (no del body) — Art. III: jamás
// confiar en lo que el cliente diga sobre su pertenencia a un tenant.
type registerDeviceRequest struct {
	Token       string `json:"token"`
	Platform    string `json:"platform"`
	DeviceLabel string `json:"device_label,omitempty"`
}

// RegisterDevice es POST /api/v1/devices/register. Es idempotente: si
// el `token` ya existe para este (tenant, user) sin invalidar, se
// devuelve el mismo `id` con `last_seen_at` refrescado. Si el token
// existe bajo OTRO tenant, la fila vieja se invalida y se crea una
// nueva (spec § 7: "un token nunca cambia de tenant").
//
// Spec: specs/038-push-notifications-web-android/spec.md — FR-06, AC-02.
func RegisterDevice(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		if tenantID == "" || userID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "no autenticado"})
			return
		}

		var req registerDeviceRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "body inválido"})
			return
		}
		req.Token = strings.TrimSpace(req.Token)
		if req.Token == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "token requerido"})
			return
		}
		if req.Platform != models.DeviceTokenPlatformWeb &&
			req.Platform != models.DeviceTokenPlatformAndroid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "platform inválida (web o android)"})
			return
		}

		now := time.Now()

		// Si el token ya existe ACTIVO en este tenant + user → refresh.
		var existing models.DeviceToken
		err := db.Where("tenant_id = ? AND user_id = ? AND token = ? AND invalidated_at IS NULL",
			tenantID, userID, req.Token).First(&existing).Error
		if err == nil {
			updates := map[string]any{"last_seen_at": now}
			if req.DeviceLabel != "" {
				updates["device_label"] = req.DeviceLabel
			}
			if err := db.Model(&existing).Updates(updates).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo actualizar el dispositivo"})
				return
			}
			c.JSON(http.StatusCreated, gin.H{"data": serializeDeviceToken(existing)})
			return
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo consultar el dispositivo"})
			return
		}

		// El token PUEDE existir bajo otro tenant (rotación de empleo).
		// Lo invalidamos antes de crear el nuevo, respetando el invariante
		// "un token nunca está activo en dos tenants a la vez".
		if err := db.Model(&models.DeviceToken{}).
			Where("token = ? AND invalidated_at IS NULL", req.Token).
			Update("invalidated_at", &now).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo limpiar el registro previo"})
			return
		}

		// Crear el nuevo registro.
		newRow := models.DeviceToken{
			TenantID:   tenantID,
			UserID:     userID,
			Token:      req.Token,
			Platform:   req.Platform,
			LastSeenAt: now,
		}
		if req.DeviceLabel != "" {
			l := req.DeviceLabel
			newRow.DeviceLabel = &l
		}
		if err := db.Create(&newRow).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo registrar el dispositivo"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": serializeDeviceToken(newRow)})
	}
}

// ListMyDevices es GET /api/v1/devices/me. Devuelve los tokens
// activos del usuario logueado, scopeados a su tenant — defensa Art.
// III: si un atacante manipula el JWT, ve solo lo de su propio tenant.
//
// Spec: specs/038-push-notifications-web-android/spec.md — FR-11.
func ListMyDevices(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		if tenantID == "" || userID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "no autenticado"})
			return
		}

		var rows []models.DeviceToken
		if err := db.Where("tenant_id = ? AND user_id = ? AND invalidated_at IS NULL",
			tenantID, userID).
			Order("last_seen_at DESC").
			Find(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudieron listar los dispositivos"})
			return
		}

		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, serializeDeviceToken(r))
		}
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}

// RevokeMyDevice es DELETE /api/v1/devices/me/:id. Soft-revoke: setea
// `invalidated_at` (no hard-delete) para mantener trazabilidad. Solo
// el dueño del token puede revocarlo; otros usuarios del mismo tenant
// reciben 404 (no 403 — no filtrar existencia).
//
// Spec: specs/038-push-notifications-web-android/spec.md — FR-11 / AC-12.
func RevokeMyDevice(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		if tenantID == "" || userID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "no autenticado"})
			return
		}

		id := c.Param("id")
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id requerido"})
			return
		}

		// Filtro triple: tenant + user + id. Si cualquiera no coincide
		// → 404 (no expone si el row existe).
		var row models.DeviceToken
		err := db.Where("id = ? AND tenant_id = ? AND user_id = ?", id, tenantID, userID).
			First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "dispositivo no encontrado"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo consultar el dispositivo"})
			return
		}

		now := time.Now()
		if err := db.Model(&row).Update("invalidated_at", &now).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo revocar el dispositivo"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// serializeDeviceToken aplica la regla "el token NUNCA viaja en la
// respuesta" — el campo `Token` lleva `json:"-"` en el modelo pero
// usamos un helper explícito para que el formato sea claro y
// versionable.
func serializeDeviceToken(t models.DeviceToken) map[string]any {
	out := map[string]any{
		"id":           t.ID,
		"platform":     t.Platform,
		"last_seen_at": t.LastSeenAt,
		"created_at":   t.CreatedAt,
	}
	if t.DeviceLabel != nil {
		out["device_label"] = *t.DeviceLabel
	}
	return out
}
