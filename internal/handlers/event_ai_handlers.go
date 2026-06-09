// Spec: specs/042-modulo-eventos/spec.md
package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"vendia-backend/internal/aiusage"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// badgeSampleName is the placeholder attendee name used when the organizer is
// designing the template (the real name + QR are filled in per attendee at
// render time on the web view).
const badgeSampleName = "Nombre del Asistente"

// GenerateEventBadgeImage — POST /api/v1/events/:id/badge/ai-generate (admin).
// Produces an escarapela design with Gemini, stores it, and saves the URL on
// the event's badge template. The Flutter editor drives the accept/reject/
// regenerate loop by calling this repeatedly (spec FR-11, AC-14).
func GenerateEventBadgeImage(db *gorm.DB, geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	return eventAssetHandler(db, geminiSvc, storageSvc, assetBadge)
}

// GenerateEventCertificateImage — POST /api/v1/events/:id/certificate/ai-generate
// (admin). Same flow as the badge, for the certificate template (FR-12).
func GenerateEventCertificateImage(db *gorm.DB, geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	return eventAssetHandler(db, geminiSvc, storageSvc, assetCertificate)
}

type eventAssetKind int

const (
	assetBadge eventAssetKind = iota
	assetCertificate
)

// eventAssetHandler is shared by the badge and certificate generators — they
// differ only in the Gemini prompt and which template field stores the URL.
func eventAssetHandler(db *gorm.DB, geminiSvc *services.GeminiService, storageSvc services.FileStorage, kind eventAssetKind) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de IA no configurado"})
			return
		}
		tenantID := middleware.GetTenantID(c)

		ev, err := services.NewEventService(db).Get(tenantID, c.Param("id"))
		if err != nil {
			writeEventError(c, err)
			return
		}

		var tenant models.Tenant
		if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
			return
		}

		ctx, cancel := context.WithTimeout(aiusage.WithTenantID(c.Request.Context(), tenantID), 60*time.Second)
		defer cancel()

		var img []byte
		if kind == assetBadge {
			img, err = geminiSvc.GenerateEventBadge(ctx, ev.Title, tenant.BusinessName, badgeSampleName)
		} else {
			img, err = geminiSvc.GenerateEventCertificate(ctx, ev.Title, tenant.BusinessName, badgeSampleName)
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("error al generar el diseño: %v", err)})
			return
		}

		// Store the design; fall back to a data URL if storage is absent so
		// the editor still gets a preview (mirrors the banner handler).
		url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img)
		if storageSvc != nil {
			key := fmt.Sprintf("events/%s/%s/%s-%s.png", tenantID, ev.ID, assetKindSlug(kind), uuid.NewString()[:8])
			if uploaded, upErr := storageSvc.Upload(ctx, "event-assets", key, img, "image/png"); upErr == nil {
				url = uploaded
			}
		}

		// Persist on the event template (struct Save applies the serializer).
		if kind == assetBadge {
			ev.BadgeTemplate.ImageURL = url
		} else {
			ev.CertificateTemplate.ImageURL = url
		}
		if err := db.Save(ev).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al guardar el diseño"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"image_url": url}})
	}
}

func assetKindSlug(kind eventAssetKind) string {
	if kind == assetBadge {
		return "badge"
	}
	return "certificate"
}
