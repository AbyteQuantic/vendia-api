// Spec: specs/042-modulo-eventos/spec.md
package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

// GenerateEventPosterImage — POST /api/v1/events/:id/poster/ai-generate (admin).
// Produces the marketing AFICHE shown in the public catalog (the WhatsApp link
// surfaces it). No QR — it sells the event. Persists on the poster template.
func GenerateEventPosterImage(db *gorm.DB, geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	return eventAssetHandler(db, geminiSvc, storageSvc, assetPoster)
}

type eventAssetKind int

const (
	assetBadge eventAssetKind = iota
	assetCertificate
	assetPoster
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

		// Optional creative direction the organizer typed in the editor. The
		// body may be empty (no Content) — ignore the bind error in that case.
		var body struct {
			Brief string `json:"brief"`
		}
		_ = c.ShouldBindJSON(&body)
		brief := strings.TrimSpace(body.Brief)

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
		switch kind {
		case assetBadge:
			img, err = geminiSvc.GenerateEventBadge(ctx, ev.Title, tenant.BusinessName, badgeSampleName, combineDescBrief(ev.Description, brief))
		case assetCertificate:
			img, err = geminiSvc.GenerateEventCertificate(ctx, ev.Title, tenant.BusinessName, badgeSampleName, combineDescBrief(ev.Description, brief))
		case assetPoster:
			img, err = geminiSvc.GenerateEventPoster(ctx, posterInputFor(ev, tenant.BusinessName, brief))
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
		switch kind {
		case assetBadge:
			ev.BadgeTemplate.ImageURL = url
		case assetCertificate:
			ev.CertificateTemplate.ImageURL = url
		case assetPoster:
			ev.PosterTemplate.ImageURL = url
		}
		if err := db.Save(ev).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al guardar el diseño"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"image_url": url}})
	}
}

func assetKindSlug(kind eventAssetKind) string {
	switch kind {
	case assetBadge:
		return "badge"
	case assetPoster:
		return "poster"
	default:
		return "certificate"
	}
}

// posterInputFor maps an event + business name into the marketing poster facts,
// formatting the date (es-CO) and price ("Gratis" / "$50.000") the way the
// catalog shows them. brief is the organizer's optional creative direction.
// Mirrors the Flutter labels so the piece reads natively.
func posterInputFor(ev *models.Event, businessName, brief string) services.PosterInput {
	in := services.PosterInput{
		Title:        ev.Title,
		BusinessName: businessName,
		Type:         ev.Type,
		TypeLabel:    eventTypeLabel(ev.Type),
		ModalityText: eventModalityLabel(ev.Modality),
		PriceText:    formatPosterPrice(ev.Price),
		Description:  ev.Description,
		Brief:        brief,
	}
	if ev.StartAt != nil {
		in.DateText = formatPosterDate(*ev.StartAt)
	}
	return in
}

// combineDescBrief merges the event's public description with the organizer's
// editor brief so the badge/certificate also reflect any creative direction.
func combineDescBrief(description, brief string) string {
	description = strings.TrimSpace(description)
	brief = strings.TrimSpace(brief)
	switch {
	case brief == "":
		return description
	case description == "":
		return brief
	default:
		return description + ". " + brief
	}
}

func eventTypeLabel(t string) string {
	switch t {
	case models.EventTypeCurso:
		return "Curso"
	case models.EventTypeConferencia:
		return "Conferencia"
	case models.EventTypeHackaton:
		return "Hackatón"
	default:
		return "Evento"
	}
}

func eventModalityLabel(m string) string {
	switch m {
	case models.EventModalityPresencial:
		return "Presencial"
	case models.EventModalityVirtual:
		return "Virtual"
	case models.EventModalityHibrido:
		return "Híbrido"
	default:
		return m
	}
}

func formatPosterPrice(price int64) string {
	if price <= 0 {
		return "Gratis"
	}
	// Thousands separator with dots (es-CO): 50000 → "$50.000".
	s := strconv.FormatInt(price, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	return "$" + b.String()
}

func formatPosterDate(t time.Time) string {
	months := []string{
		"enero", "febrero", "marzo", "abril", "mayo", "junio",
		"julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre",
	}
	return fmt.Sprintf("%d de %s de %d", t.Day(), months[int(t.Month())-1], t.Year())
}
