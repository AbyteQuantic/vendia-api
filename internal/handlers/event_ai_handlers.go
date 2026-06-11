// Spec: specs/042-modulo-eventos/spec.md
package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
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

// GenerateEventDescriptionAI — POST /api/v1/event-description-ai (admin).
// Agente que redacta la descripción del evento a partir de lo que el
// organizador respondió (audiencia, qué incluye, nivel…). No requiere id del
// evento: sirve al crear y al editar. Ruta propia para no chocar con
// /events/:id (Gin no mezcla :id y segmento estático al mismo nivel).
func GenerateEventDescriptionAI(geminiSvc *services.GeminiService) gin.HandlerFunc {
	type request struct {
		Title    string `json:"title"`
		Type     string `json:"type"`
		Modality string `json:"modality"`
		Topic    string `json:"topic"`
		Audience string `json:"audience"`
		Includes string `json:"includes"`
		Level    string `json:"level"`
		Place    string `json:"place"`
		Extra    string `json:"extra"`
		Current  string `json:"current"`
	}
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de IA no configurado"})
			return
		}
		var req request
		_ = c.ShouldBindJSON(&req)
		if strings.TrimSpace(req.Title) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ponle primero un nombre al evento"})
			return
		}
		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), middleware.GetTenantID(c)), 30*time.Second)
		defer cancel()
		text, err := geminiSvc.GenerateText(ctx, services.BuildEventDescriptionPrompt(
			services.EventDescriptionInput{
				Title: req.Title, Type: req.Type, Modality: req.Modality,
				Topic: req.Topic, Audience: req.Audience, Includes: req.Includes,
				Level: req.Level, Place: req.Place, Extra: req.Extra,
				Current: req.Current,
			}))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no pudimos generar la descripción, intenta de nuevo"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"description": text}})
	}
}

// CleanEventSignature — POST /api/v1/event-signature-clean (admin). Recibe la
// foto/imagen de la firma, la limpia con IA (aísla los trazos, quita el fondo)
// y devuelve la URL lista para el certificado. Ruta propia (sin id de evento).
func CleanEventSignature(geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de IA no configurado"})
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
		mime := header.Header.Get("Content-Type")
		if !strings.HasPrefix(mime, "image/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el archivo debe ser una imagen"})
			return
		}
		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer la imagen"})
			return
		}

		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), tenantID), 60*time.Second)
		defer cancel()
		cleaned, err := geminiSvc.CleanSignature(ctx, data, mime)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no pudimos limpiar la firma, intenta de nuevo"})
			return
		}

		// Gemini devuelve la firma sobre fondo BLANCO (no sabe emitir alfa).
		// Quitamos el fondo aquí → PNG con transparencia real, sin recuadro
		// blanco (IMG_4096). Si fallara el recorte, usamos la versión de IA.
		if transparent, terr := services.MakeSignatureTransparent(cleaned); terr == nil {
			cleaned = transparent
		}

		url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(cleaned)
		if storageSvc != nil {
			key := fmt.Sprintf("events/%s/signature/%s", tenantID, uuid.NewString()[:8])
			if up, e := storageSvc.Upload(ctx, "event-assets", key, cleaned, "image/png"); e == nil {
				url = up
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"url": url}})
	}
}

// RemoveEventLogoBackground — POST /api/v1/event-logo-remove-bg (admin). Recibe
// la URL del logo actual del certificado, le quita SOLO el fondo conectado a los
// bordes (deja intactos los colores y los blancos internos del logo) y devuelve
// un PNG transparente liviano. A diferencia de la firma, no pasa por Gemini: es
// recorte determinista (flood-fill), más preciso y sin costo de IA (FR-12).
func RemoveEventLogoBackground(storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireEventAdmin(c) {
			return
		}
		tenantID := middleware.GetTenantID(c)

		url := strings.TrimSpace(c.PostForm("url"))
		if url == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "falta la URL del logo (campo: url)"})
			return
		}

		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), tenantID), 30*time.Second)
		defer cancel()

		data, _, err := fetchEventImageBytes(ctx, url)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no pudimos leer el logo actual"})
			return
		}

		out, err := services.RemoveLogoBackground(data)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "no pudimos quitar el fondo del logo, intenta con otra imagen"})
			return
		}

		resultURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(out)
		if storageSvc != nil {
			key := fmt.Sprintf("events/%s/logo/%s.png", tenantID, uuid.NewString()[:8])
			if up, e := storageSvc.Upload(ctx, "event-assets", key, out, "image/png"); e == nil {
				resultURL = up
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"url": resultURL}})
	}
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
			img, err = geminiSvc.GenerateEventBadge(ctx, ev.Title, tenant.BusinessName, combineDescBrief(ev.Description, brief))
		case assetCertificate:
			img, err = geminiSvc.GenerateEventCertificate(ctx, ev.Title, tenant.BusinessName, combineDescBrief(ev.Description, brief))
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

// currentAssetURL returns the event's stored image URL for a piece.
func currentAssetURL(ev *models.Event, kind eventAssetKind) string {
	switch kind {
	case assetBadge:
		return ev.BadgeTemplate.ImageURL
	case assetCertificate:
		return ev.CertificateTemplate.ImageURL
	default:
		return ev.PosterTemplate.ImageURL
	}
}

// serviceAssetKind maps the handler enum to the Gemini service enum.
func serviceAssetKind(kind eventAssetKind) services.EventAssetKind {
	switch kind {
	case assetBadge:
		return services.AssetBadge
	case assetCertificate:
		return services.AssetCertificate
	default:
		return services.AssetPoster
	}
}

// fetchEventImageBytes reads an event piece's current image into bytes — both a
// data URL (base64 inline) and an http(s) URL (stored in R2).
func fetchEventImageBytes(ctx context.Context, url string) ([]byte, string, error) {
	if strings.HasPrefix(url, "data:") {
		comma := strings.IndexByte(url, ',')
		if comma < 0 {
			return nil, "", fmt.Errorf("data URL inválida")
		}
		meta := url[5:comma] // e.g. image/png;base64
		mime := "image/png"
		if semi := strings.IndexByte(meta, ';'); semi > 0 {
			mime = meta[:semi]
		} else if meta != "" {
			mime = meta
		}
		data, err := base64.StdEncoding.DecodeString(url[comma+1:])
		if err != nil {
			return nil, "", err
		}
		return data, mime, nil
	}
	return downloadSourceImage(ctx, url)
}

// GenerateEventBadgeEnhance — POST /api/v1/events/:id/badge/ai-enhance (admin).
// Mejora con IA la imagen actual de la escarapela (subida o generada).
func GenerateEventBadgeEnhance(db *gorm.DB, geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	return eventAssetEnhanceHandler(db, geminiSvc, storageSvc, assetBadge)
}

// GenerateEventCertificateEnhance — POST /api/v1/events/:id/certificate/ai-enhance (admin).
func GenerateEventCertificateEnhance(db *gorm.DB, geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	return eventAssetEnhanceHandler(db, geminiSvc, storageSvc, assetCertificate)
}

// GenerateEventPosterEnhance — POST /api/v1/events/:id/poster/ai-enhance (admin).
func GenerateEventPosterEnhance(db *gorm.DB, geminiSvc *services.GeminiService, storageSvc services.FileStorage) gin.HandlerFunc {
	return eventAssetEnhanceHandler(db, geminiSvc, storageSvc, assetPoster)
}

// eventAssetEnhanceHandler improves the piece's CURRENT image with Gemini
// (image-to-image), like the inventory photo enhancer. Requires an existing
// image; preserves its content and persists the improved version.
func eventAssetEnhanceHandler(db *gorm.DB, geminiSvc *services.GeminiService, storageSvc services.FileStorage, kind eventAssetKind) gin.HandlerFunc {
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
		source := currentAssetURL(ev, kind)
		if source == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no hay imagen para mejorar; genere o suba una primero"})
			return
		}

		// Indicaciones opcionales del organizador (campo "brief", multipart).
		// Si vienen, la IA RECREA la escena siguiéndolas; si no, retoca.
		brief := strings.TrimSpace(c.PostForm("brief"))

		ctx, cancel := context.WithTimeout(aiusage.WithTenantID(c.Request.Context(), tenantID), 60*time.Second)
		defer cancel()

		data, mime, err := fetchEventImageBytes(ctx, source)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no pudimos leer la imagen actual"})
			return
		}
		images := []services.ReferenceImage{{MimeType: mime, Data: data}}

		// Foto de rostro opcional (campo "reference"): ancla la identidad de la
		// persona para que la cara salga idéntica.
		if file, header, ferr := c.Request.FormFile("reference"); ferr == nil {
			defer file.Close()
			if header.Size <= maxEventAssetBytes {
				refMime := header.Header.Get("Content-Type")
				if strings.HasPrefix(refMime, "image/") {
					if refData, rerr := io.ReadAll(file); rerr == nil {
						images = append(images, services.ReferenceImage{MimeType: refMime, Data: refData})
					}
				}
			}
		}

		enhanced, err := geminiSvc.EnhanceEventAsset(ctx, serviceAssetKind(kind), brief, images)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("error al mejorar la imagen: %v", err)})
			return
		}

		url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(enhanced)
		if storageSvc != nil {
			key := fmt.Sprintf("events/%s/%s/%s-%s.png", tenantID, ev.ID, assetKindSlug(kind), uuid.NewString()[:8])
			if uploaded, upErr := storageSvc.Upload(ctx, "event-assets", key, enhanced, "image/png"); upErr == nil {
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
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al guardar la imagen mejorada"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"image_url": url}})
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
