package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func GenerateLogo(db *gorm.DB, geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de IA no configurado"})
			return
		}
		if storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de almacenamiento no configurado"})
			return
		}

		// Accept JSON body with business_name / business_type
		var req struct {
			BusinessName string `json:"business_name"`
			BusinessType string `json:"business_type"`
		}
		_ = c.ShouldBindJSON(&req)

		// Fallback to tenant data if not provided
		if req.BusinessName == "" || req.BusinessType == "" {
			var tenant models.Tenant
			if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
				return
			}
			if req.BusinessName == "" {
				req.BusinessName = tenant.BusinessName
			}
			if req.BusinessType == "" && len(tenant.BusinessTypes) > 0 {
				req.BusinessType = tenant.BusinessTypes[0]
			}
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
		defer cancel()

		logos, err := geminiSvc.GenerateLogo(ctx, req.BusinessName, req.BusinessType)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("error al generar logo: %v", err)})
			return
		}
		if len(logos) == 0 {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "la IA no generó ninguna imagen"})
			return
		}

		// Upload to store-logos bucket in Supabase
		logo := logos[0]
		key := fmt.Sprintf("logos/%s/ai-%s.webp", tenantID, uuid.NewString()[:8])
		logoURL, err := storageSvc.Upload(ctx, "store-logos", key, logo.ImageData, "image/webp")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al guardar logo generado"})
			return
		}

		// Persist logo_url on tenant
		db.Model(&models.Tenant{}).Where("id = ?", tenantID).Update("logo_url", logoURL)

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"logo_url": logoURL}})
	}
}

func UploadLogo(db *gorm.DB, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		file, header, err := c.Request.FormFile("logo")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "logo requerido (campo: logo)"})
			return
		}
		defer file.Close()

		if header.Size > 2<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "logo excede 2MB"})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer logo"})
			return
		}

		if storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de almacenamiento no configurado"})
			return
		}

		mimeType := header.Header.Get("Content-Type")
		key := fmt.Sprintf("logos/%s/custom-%s.webp", tenantID, uuid.NewString()[:8])

		logoURL, err := storageSvc.Upload(c.Request.Context(), "vendia-logos", key, data, mimeType)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al subir logo"})
			return
		}

		db.Model(&models.Tenant{}).Where("id = ?", tenantID).Update("logo_url", logoURL)

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"logo_url": logoURL}})
	}
}
