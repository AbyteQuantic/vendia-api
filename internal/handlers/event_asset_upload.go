// Spec: specs/042-modulo-eventos/spec.md
package handlers

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// maxEventAssetBytes caps an uploaded event piece. Posters can be richer than a
// product photo, so we allow up to 8MB before rejecting.
const maxEventAssetBytes = 8 << 20

// UploadEventPosterImage — POST /api/v1/events/:id/poster/upload (admin).
// Lets the organizer use THEIR OWN image for the catalog poster instead of the
// AI generator. Same persistence target as the AI path (poster template), so
// the public catalog and the WhatsApp link surface whichever the organizer
// chose last (FR-11/FR-13: AI assist is optional).
func UploadEventPosterImage(db *gorm.DB, storageSvc services.FileStorage) gin.HandlerFunc {
	return eventAssetUploadHandler(db, storageSvc, assetPoster)
}

// UploadEventBadgeImage — POST /api/v1/events/:id/badge/upload (admin).
func UploadEventBadgeImage(db *gorm.DB, storageSvc services.FileStorage) gin.HandlerFunc {
	return eventAssetUploadHandler(db, storageSvc, assetBadge)
}

// UploadEventCertificateImage — POST /api/v1/events/:id/certificate/upload (admin).
func UploadEventCertificateImage(db *gorm.DB, storageSvc services.FileStorage) gin.HandlerFunc {
	return eventAssetUploadHandler(db, storageSvc, assetCertificate)
}

// UploadEventPaymentQR — POST /api/v1/event-payment-qr (admin). Sube la imagen
// del QR de un medio de pago y DEVUELVE su URL (no la persiste en un evento),
// para incluirla en payment_details al guardar. Así funciona tanto al crear
// (aún sin id) como al editar. Fallback a data URL si no hay storage.
func UploadEventPaymentQR(storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		tenantID := middleware.GetTenantID(c)

		file, header, err := c.Request.FormFile("image")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "imagen requerida (campo: image)"})
			return
		}
		defer file.Close()
		if header.Size > maxEventAssetBytes {
			c.JSON(http.StatusBadRequest, gin.H{"error": "la imagen excede 8MB"})
			return
		}
		mimeType := header.Header.Get("Content-Type")
		if !strings.HasPrefix(mimeType, "image/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el archivo debe ser una imagen"})
			return
		}
		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer la imagen"})
			return
		}

		url := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
		if storageSvc != nil {
			key := fmt.Sprintf("events/%s/payment-qr/%s", tenantID, uuid.NewString()[:8])
			if uploaded, upErr := storageSvc.Upload(c.Request.Context(), "event-assets", key, data, mimeType); upErr == nil {
				url = uploaded
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"url": url}})
	}
}

// eventAssetUploadHandler is shared by the three "upload your own" endpoints —
// they differ only in which template field stores the URL. Mirrors the product
// photo upload, with a data-URL fallback so it still works when object storage
// is momentarily absent (same degradation as the AI generator).
func eventAssetUploadHandler(db *gorm.DB, storageSvc services.FileStorage, kind eventAssetKind) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		tenantID := middleware.GetTenantID(c)

		ev, err := services.NewEventService(db).Get(tenantID, c.Param("id"))
		if err != nil {
			writeEventError(c, err)
			return
		}

		file, header, err := c.Request.FormFile("image")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "imagen requerida (campo: image)"})
			return
		}
		defer file.Close()

		if header.Size > maxEventAssetBytes {
			c.JSON(http.StatusBadRequest, gin.H{"error": "la imagen excede 8MB"})
			return
		}

		mimeType := header.Header.Get("Content-Type")
		if !strings.HasPrefix(mimeType, "image/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el archivo debe ser una imagen"})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer la imagen"})
			return
		}

		// Store the image; fall back to a data URL if storage is absent so the
		// editor still gets a preview (mirrors the AI generator handler).
		url := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
		if storageSvc != nil {
			key := fmt.Sprintf("events/%s/%s/%s-%s", tenantID, ev.ID, assetKindSlug(kind), uuid.NewString()[:8])
			if uploaded, upErr := storageSvc.Upload(c.Request.Context(), "event-assets", key, data, mimeType); upErr == nil {
				url = uploaded
			}
		}

		switch kind {
		case assetBadge:
			ev.BadgeTemplate.ImageURL = url
		case assetCertificate:
			ev.CertificateTemplate.ImageURL = url
		case assetPoster:
			ev.PosterTemplate.ImageURL = url
		}
		if err := db.Save(ev).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al guardar la imagen"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"image_url": url}})
	}
}
