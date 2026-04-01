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

		var tenant models.Tenant
		if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
		defer cancel()

		// Use query param "types" if provided (comma-separated), fallback to tenant's type
		businessTypeStr := c.Query("types")
		if businessTypeStr == "" {
			businessTypeStr = string(tenant.BusinessType)
		}
		logos, err := geminiSvc.GenerateLogo(ctx, tenant.BusinessName, businessTypeStr)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("error al generar logos: %v", err)})
			return
		}

		type LogoOption struct {
			URL string `json:"url"`
		}

		var options []LogoOption
		for i, logo := range logos {
			if storageSvc != nil {
				key := fmt.Sprintf("logos/%s/option-%d-%s.webp", tenantID, i+1, uuid.NewString()[:8])
				url, err := storageSvc.Upload(ctx, "vendia-logos", key, logo.ImageData, "image/webp")
				if err == nil {
					options = append(options, LogoOption{URL: url})
					continue
				}
			}
			options = append(options, LogoOption{URL: fmt.Sprintf("data:%s;base64,generated", logo.MimeType)})
		}

		c.JSON(http.StatusOK, gin.H{"data": options})
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
