// Spec: specs/038-push-notifications-web-android/spec.md
package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services/push"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// registerDeviceRequest es el contrato del POST /devices/register.
// El tenant y el user vienen del JWT (no del body) — Art. III.
//
// Soporta dos modos:
//   - FCM: cliente envía `token` (web/android con firebase_messaging).
//   - Web Push nativo: cliente envía `endpoint` + `p256dh_key` + `auth_key`
//     (iOS Safari, donde firebase_messaging no funciona).
//
// Al menos uno de los dos modos debe estar completo; el handler valida.
type registerDeviceRequest struct {
	Token       string `json:"token,omitempty"`
	Platform    string `json:"platform"`
	DeviceLabel string `json:"device_label,omitempty"`

	Endpoint    string `json:"endpoint,omitempty"`
	P256dhKey   string `json:"p256dh_key,omitempty"`
	AuthKey     string `json:"auth_key,omitempty"`
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
		req.Endpoint = strings.TrimSpace(req.Endpoint)
		req.P256dhKey = strings.TrimSpace(req.P256dhKey)
		req.AuthKey = strings.TrimSpace(req.AuthKey)

		hasFCM := req.Token != ""
		hasWebPush := req.Endpoint != "" && req.P256dhKey != "" && req.AuthKey != ""
		if !hasFCM && !hasWebPush {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "se requiere `token` (FCM) o `endpoint`+`p256dh_key`+`auth_key` (Web Push)",
			})
			return
		}
		switch req.Platform {
		case models.DeviceTokenPlatformWeb,
			models.DeviceTokenPlatformWebIOS,
			models.DeviceTokenPlatformAndroid:
		default:
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "platform inválida (web, web_ios o android)",
			})
			return
		}

		now := time.Now()

		// Lookup por la clave única del modo elegido.
		var existing models.DeviceToken
		var lookupQuery *gorm.DB
		if hasFCM {
			lookupQuery = db.Where(
				"tenant_id = ? AND user_id = ? AND token = ? AND invalidated_at IS NULL",
				tenantID, userID, req.Token)
		} else {
			lookupQuery = db.Where(
				"tenant_id = ? AND user_id = ? AND endpoint = ? AND invalidated_at IS NULL",
				tenantID, userID, req.Endpoint)
		}
		err := lookupQuery.First(&existing).Error
		if err == nil {
			updates := map[string]any{"last_seen_at": now}
			if req.DeviceLabel != "" {
				updates["device_label"] = req.DeviceLabel
			}
			// Refresh las claves Web Push (pueden rotar).
			if hasWebPush {
				updates["p256dh"] = req.P256dhKey
				updates["auth"] = req.AuthKey
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

		// Invalidar fila anterior bajo OTRO tenant (rotación de empleo).
		if hasFCM {
			if err := db.Model(&models.DeviceToken{}).
				Where("token = ? AND invalidated_at IS NULL", req.Token).
				Update("invalidated_at", &now).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo limpiar el registro previo"})
				return
			}
		} else {
			if err := db.Model(&models.DeviceToken{}).
				Where("endpoint = ? AND invalidated_at IS NULL", req.Endpoint).
				Update("invalidated_at", &now).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo limpiar el registro previo"})
				return
			}
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
		if hasWebPush {
			ep, p, a := req.Endpoint, req.P256dhKey, req.AuthKey
			newRow.Endpoint = &ep
			newRow.P256dh = &p
			newRow.Auth = &a
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

// TestPushSelf es POST /api/v1/devices/me/test. Dispara un push de
// prueba al tenant del usuario logueado. Sirve para que el tendero
// pueda verificar in-situ que su dispositivo está bien configurado
// sin tener que esperar a que ocurra un evento real (pedido, abono).
//
// Comparte el mismo dispatcher que los triggers automáticos, así que
// respeta TODAS las reglas (suspensión, cap diario, dedup). El
// dedup_key incluye el `user_id` + timestamp para permitir múltiples
// pruebas del mismo dueño sin chocar con la ventana de 5 min.
//
// Spec: specs/038-push-notifications-web-android/spec.md — extension
// del FR-11 (settings de notificaciones).
func TestPushSelf(db *gorm.DB, dispatcher *push.Dispatcher) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		if tenantID == "" || userID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "no autenticado"})
			return
		}
		if dispatcher == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "el servicio de notificaciones no está configurado",
			})
			return
		}

		outcome, err := dispatcher.DispatchEvent(c.Request.Context(), db, push.Event{
			TenantID: tenantID,
			Type:     "self_test",
			Title:    "¡Funciona! Notificación de prueba",
			Body:     "Si lo ve, su dispositivo recibe notificaciones correctamente.",
			DeepLink: "/",
			// UUID nuevo por llamada → cada prueba es un evento único
			// (no choca con la ventana de dedup de 5 min).
			DedupKey: "self-test:" + userID + ":" + uuid.NewString(),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo enviar la prueba",
			})
			return
		}

		c.JSON(http.StatusAccepted, gin.H{"data": gin.H{
			"status":          outcome.Status,
			"tokens_targeted": outcome.TokensSent,
		}})
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
