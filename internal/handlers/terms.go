// Spec: specs/098-aporte-automatico-fotos-colaborativo/spec.md
package handlers

import (
	"net/http"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AcceptTerms — POST /api/v1/terms/accept. Registra que el tenant AUTENTICADO
// aceptó la versión vigente de los Términos y Servicios (Spec 098, incluye la
// cláusula de uso colaborativo de imágenes). Idempotente: re-aceptar sólo
// refresca la fecha/versión. Habilita el aporte automático (Fase 2).
func AcceptTerms(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if tenantID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "no autorizado"})
			return
		}
		now := time.Now()
		if err := db.Model(&models.Tenant{}).
			Where("id = ?", tenantID).
			Updates(map[string]any{
				"terms_accepted_version": models.CatalogTermsVersion,
				"terms_accepted_at":      now,
			}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo registrar la aceptación"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message":       "términos aceptados",
			"terms_version": models.CatalogTermsVersion,
		})
	}
}
