package handlers

import (
	"net/http"
	"regexp"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var pinRegexp = regexp.MustCompile(`^\d{4}$`)

// SetOwnerPin — POST /api/v1/tenant/owner-pin
// Owner sets/updates the 4-digit PIN cashiers use to authorize sensitive
// actions (new fiado for unknown customers, void past sales, etc.).
// Only callable by owner/admin roles.
func SetOwnerPin(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Pin string `json:"pin" binding:"required"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		role := middleware.GetRole(c)

		// Legacy tokens have empty role — treat as owner for back-compat.
		if role != "" && role != "owner" && role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "solo el propietario puede configurar el PIN"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "pin requerido"})
			return
		}
		if !pinRegexp.MatchString(req.Pin) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el PIN debe ser exactamente 4 dígitos"})
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(req.Pin), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al guardar el PIN"})
			return
		}

		if err := db.Model(&models.Tenant{}).
			Where("id = ?", tenantID).
			Update("owner_pin_hash", string(hash)).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al guardar el PIN"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"message": "PIN guardado"}})
	}
}

// VerifyOwnerPin — POST /api/v1/tenant/owner-pin/verify
// Cashier submits the PIN the owner just dictated out loud to unlock one
// restricted action (e.g. new fiado for an unknown customer). The server
// compares against the hashed PIN for the current tenant.
func VerifyOwnerPin(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Pin string `json:"pin" binding:"required"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "pin requerido"})
			return
		}
		if !pinRegexp.MatchString(req.Pin) {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "pin inválido"})
			return
		}

		var tenant models.Tenant
		if err := db.Select("owner_pin_hash").
			Where("id = ?", tenantID).
			First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "negocio no encontrado"})
			return
		}
		if tenant.OwnerPinHash == "" {
			c.JSON(http.StatusPreconditionFailed, gin.H{
				"ok":    false,
				"error": "el propietario aún no ha configurado un PIN",
			})
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(tenant.OwnerPinHash), []byte(req.Pin)); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "error": "PIN incorrecto"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}
