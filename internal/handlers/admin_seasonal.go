// Spec: specs/086-branding-estacional/spec.md
package handlers

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var hexRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
var keyRe = regexp.MustCompile(`^[a-z0-9_]{2,40}$`)

// seasonalCampaignReq — payload de create/update (campos opcionales como punteros
// para distinguir "no-override" de vacío).
type seasonalCampaignReq struct {
	Key         *string    `json:"key"`
	Name        *string    `json:"name"`
	Enabled     *bool      `json:"enabled"`
	ForceActive *bool      `json:"force_active"`
	Priority    *int       `json:"priority"`
	StartsAt    *time.Time `json:"starts_at"`
	EndsAt      *time.Time `json:"ends_at"`
	AccentHex   *string    `json:"accent_hex"`
	SplashBgHex *string    `json:"splash_bg_hex"`
	SplashImageURL *string `json:"splash_image_url"`
	SplashMessage  *string `json:"splash_message"`
	BannerText     *string `json:"banner_text"`
	BannerImageURL *string `json:"banner_image_url"`
	BannerBgHex    *string `json:"banner_bg_hex"`
	BannerLinkURL  *string `json:"banner_link_url"`
	IconVariant    *string `json:"icon_variant"`
}

// validateSeasonal valida hex, fechas, icon_variant y URLs. Devuelve mensaje en
// español o "" si todo OK.
func validateSeasonal(r seasonalCampaignReq, requireKey bool) string {
	if requireKey {
		if r.Key == nil || !keyRe.MatchString(*r.Key) {
			return "key inválida (use minúsculas/números/guion_bajo, 2–40)"
		}
	}
	for _, h := range []*string{r.AccentHex, r.SplashBgHex, r.BannerBgHex} {
		if h != nil && *h != "" && !hexRe.MatchString(*h) {
			return "color inválido (use #RRGGBB)"
		}
	}
	for _, u := range []*string{r.SplashImageURL, r.BannerImageURL, r.BannerLinkURL} {
		if u != nil && *u != "" && !strings.HasPrefix(*u, "https://") {
			return "las URLs deben iniciar con https://"
		}
	}
	if r.IconVariant != nil {
		if _, ok := models.IconVariants[*r.IconVariant]; !ok {
			return "icon_variant no permitido"
		}
	}
	if r.StartsAt != nil && r.EndsAt != nil && !r.StartsAt.Before(*r.EndsAt) {
		return "la fecha de inicio debe ser anterior a la de fin"
	}
	return ""
}

func applySeasonalReq(c *models.SeasonalCampaign, r seasonalCampaignReq) {
	if r.Key != nil {
		c.Key = *r.Key
	}
	if r.Name != nil {
		c.Name = *r.Name
	}
	if r.Enabled != nil {
		c.Enabled = *r.Enabled
	}
	if r.ForceActive != nil {
		c.ForceActive = *r.ForceActive
	}
	if r.Priority != nil {
		c.Priority = *r.Priority
	}
	if r.IconVariant != nil {
		c.IconVariant = *r.IconVariant
	}
	// Fechas y overrides (punteros se asignan tal cual: nil = limpiar override).
	c.StartsAt = r.StartsAt
	c.EndsAt = r.EndsAt
	c.AccentHex = r.AccentHex
	c.SplashBgHex = r.SplashBgHex
	c.SplashImageURL = r.SplashImageURL
	c.SplashMessage = r.SplashMessage
	c.BannerText = r.BannerText
	c.BannerImageURL = r.BannerImageURL
	c.BannerBgHex = r.BannerBgHex
	c.BannerLinkURL = r.BannerLinkURL
}

// AdminListSeasonalCampaigns — GET /api/v1/admin/seasonal-campaigns
func AdminListSeasonalCampaigns(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var rows []models.SeasonalCampaign
		db.Order("priority desc, starts_at desc, created_at desc").Find(&rows)
		c.JSON(http.StatusOK, gin.H{"data": rows})
	}
}

// AdminCreateSeasonalCampaign — POST /api/v1/admin/seasonal-campaigns
func AdminCreateSeasonalCampaign(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var r seasonalCampaignReq
		if err := c.ShouldBindJSON(&r); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if msg := validateSeasonal(r, true); msg != "" {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": msg})
			return
		}
		var camp models.SeasonalCampaign
		camp.IconVariant = "default"
		applySeasonalReq(&camp, r)
		if err := db.Create(&camp).Error; err != nil {
			if isRetryableConflict(err) {
				c.JSON(http.StatusConflict, gin.H{"error": "ya existe una campaña con esa key"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo crear"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": camp})
	}
}

// AdminUpdateSeasonalCampaign — PATCH /api/v1/admin/seasonal-campaigns/:id
func AdminUpdateSeasonalCampaign(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var camp models.SeasonalCampaign
		if err := db.Where("id = ?", c.Param("id")).First(&camp).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "campaña no encontrada"})
			return
		}
		var r seasonalCampaignReq
		if err := c.ShouldBindJSON(&r); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if msg := validateSeasonal(r, false); msg != "" {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": msg})
			return
		}
		applySeasonalReq(&camp, r)
		if err := db.Save(&camp).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo actualizar"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": camp})
	}
}

// AdminDeleteSeasonalCampaign — DELETE /api/v1/admin/seasonal-campaigns/:id
func AdminDeleteSeasonalCampaign(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		db.Where("id = ?", c.Param("id")).Delete(&models.SeasonalCampaign{})
		c.JSON(http.StatusOK, gin.H{"message": "campaña eliminada"})
	}
}

// AdminActivateSeasonalCampaign — POST /api/v1/admin/seasonal-campaigns/:id/activate
// Toggle rápido de enabled + force_active (lanzar/QA o apagar de un clic).
func AdminActivateSeasonalCampaign(db *gorm.DB) gin.HandlerFunc {
	type Req struct {
		Enabled     bool `json:"enabled"`
		ForceActive bool `json:"force_active"`
	}
	return func(c *gin.Context) {
		var req Req
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		res := db.Model(&models.SeasonalCampaign{}).
			Where("id = ?", c.Param("id")).
			Updates(map[string]any{"enabled": req.Enabled, "force_active": req.ForceActive})
		if res.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "campaña no encontrada"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "campaña actualizada"})
	}
}
