// Spec: specs/086-branding-estacional/spec.md
package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// brandingLoc — hora Colombia (sin DST). La temporada arranca a medianoche local.
var brandingLoc = time.FixedZone("America/Bogota", -5*60*60)

func seasonETag(active bool, c models.SeasonalCampaign) string {
	raw := "none"
	if active {
		raw = fmt.Sprintf("%s-%d-%t", c.Key, c.UpdatedAt.UnixNano(), c.ForceActive)
	}
	sum := sha256.Sum256([]byte(raw))
	return `"` + hex.EncodeToString(sum[:12]) + `"`
}

func strv(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// GetSeasonalBranding — GET /api/v1/branding/season (PÚBLICO, pre-login).
// Devuelve la temporada activa o {active:false}. ETag/304. FAIL-CLOSED: ante
// cualquier error responde 200 {active:false} (nunca 500 — rompería el splash).
func GetSeasonalBranding(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		respondNone := func() {
			etag := seasonETag(false, models.SeasonalCampaign{})
			if match := c.GetHeader("If-None-Match"); match != "" && match == etag {
				c.Status(http.StatusNotModified)
				return
			}
			c.Header("ETag", etag)
			c.Header("Cache-Control", "public, max-age=300, must-revalidate")
			c.JSON(http.StatusOK, gin.H{"data": gin.H{"active": false}})
		}

		var rows []models.SeasonalCampaign
		if err := db.Where("enabled = ?", true).Find(&rows).Error; err != nil {
			respondNone() // fail-closed
			return
		}
		active, ok := services.ResolveActiveCampaign(rows, time.Now().In(brandingLoc))
		if !ok {
			respondNone()
			return
		}

		etag := seasonETag(true, active)
		if match := c.GetHeader("If-None-Match"); match != "" && match == etag {
			c.Status(http.StatusNotModified)
			return
		}
		c.Header("ETag", etag)
		c.Header("Cache-Control", "public, max-age=300, must-revalidate")
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"active":       true,
			"key":          active.Key,
			"name":         active.Name,
			"accent_hex":   strv(active.AccentHex),
			"icon_variant": active.IconVariant,
			"splash": gin.H{
				"bg_hex":    strv(active.SplashBgHex),
				"image_url": strv(active.SplashImageURL),
				"message":   strv(active.SplashMessage),
			},
			"banner": gin.H{
				"text":      strv(active.BannerText),
				"image_url": strv(active.BannerImageURL),
				"bg_hex":    strv(active.BannerBgHex),
				"link_url":  strv(active.BannerLinkURL),
			},
		}})
	}
}
