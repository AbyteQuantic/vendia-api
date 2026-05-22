// Spec: specs/033-difusion-promociones/spec.md
package handlers

import (
	"fmt"
	"io"
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UploadBroadcastPromotionImage receives the photo/banner the merchant wants
// to attach to a broadcast promotion (F033) and stores it in R2.
//
// Mirrors UploadLogo: reads the multipart `image` field, caps at 2MB, sniffs
// the real format with detectImageType (so HEIC iPhone photos are rejected
// with a clear 400 instead of a generic 500) and uploads to the shared
// product-photos bucket. The new public URL is returned to the client, which
// then persists it on the promotion via create/update.
func UploadBroadcastPromotionImage(db *gorm.DB, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		file, header, err := c.Request.FormFile("image")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "imagen requerida (campo: image)"})
			return
		}
		defer file.Close()

		if header.Size > 2<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "imagen excede 2MB"})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer imagen"})
			return
		}

		if storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de almacenamiento no configurado"})
			return
		}

		// Sniff the real image format from the bytes instead of trusting
		// the client Content-Type. iPhone photos arrive as HEIC and would
		// otherwise fail the upload with a generic 500.
		mimeType := detectImageType(data)
		if !uploadableImageTypes[mimeType] {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      logoFormatoNoSoportadoMsg,
				"error_code": logoFormatoNoSoportadoCode,
			})
			return
		}

		key := fmt.Sprintf("promos/%s/%s.webp", tenantID, uuid.NewString())

		imageURL, err := storageSvc.Upload(c.Request.Context(), "product-photos", key, data, mimeType)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al subir imagen"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"image_url": imageURL}})
	}
}
