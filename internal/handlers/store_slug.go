// store_slug.go — dedicated store slug endpoints.
//
// Why this lives next to store.go instead of inside it:
//   - GetStoreConfig is a grab-bag object that the Settings screen
//     loads on mount; the Marketing Hub needs a focused GET that also
//     auto-provisions a slug (and returns the full public URL) when
//     the tenant hasn't chosen one yet. Keeping the handler separate
//     avoids widening the existing config blob.
//   - UpdateStoreConfig's slug branch is lenient (just checks
//     uniqueness). The Marketing Hub flow must also enforce the
//     URL-safe regex, so we centralize that validation here and leave
//     the legacy endpoint untouched for backward-compat.
package handlers

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// defaultPublicStoreBase is the fallback public URL prefix when
// PUBLIC_STORE_URL is not set in the environment. Matches the
// existing `https://vendia-admin.vercel.app` convention used by the
// fiado links so tenants don't get a different hostname per feature.
const defaultPublicStoreBase = "https://vendia-admin.vercel.app"

// publicStoreBaseURL resolves the base URL the client should use to
// build the public catalog link. Reads PUBLIC_STORE_URL from env so
// staging/prod can override without a code change.
func publicStoreBaseURL() string {
	if v := strings.TrimRight(os.Getenv("PUBLIC_STORE_URL"), "/"); v != "" {
		return v
	}
	return defaultPublicStoreBase
}

// slugResponse is the wire shape returned by both GET and PATCH.
// Exposed as a type so the Flutter client and the admin web share a
// single contract. base_url is always absolute; public_url is
// base_url + "/" + slug (so the client never concatenates itself).
type slugResponse struct {
	Slug      string `json:"slug"`
	BaseURL   string `json:"base_url"`
	PublicURL string `json:"public_url"`
}

func buildSlugResponse(slug string) slugResponse {
	base := publicStoreBaseURL()
	return slugResponse{
		Slug:      slug,
		BaseURL:   base,
		PublicURL: base + "/" + slug,
	}
}

// GetStoreSlug returns the tenant's store slug, auto-generating one
// from the business name if it's missing.
//
// GET /api/v1/store/slug
//
// Response 200:
//
//	{"data": {"slug": "tienda-don-pepe-a4x9",
//	          "base_url": "https://vendia-admin.vercel.app",
//	          "public_url": "https://vendia-admin.vercel.app/tienda-don-pepe-a4x9"}}
func GetStoreSlug(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var tenant models.Tenant
		if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
			return
		}

		// Auto-provision on first read. We persist the generated slug
		// so subsequent calls are stable (tenants would otherwise see
		// a different URL on every refresh, which wrecks the "compartir
		// catálogo" contract — the link a customer received yesterday
		// must keep working).
		if tenant.StoreSlug == nil || *tenant.StoreSlug == "" {
			generated, err := services.GenerateUniqueSlug(db, tenant.BusinessName, tenant.ID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if err := db.Model(&models.Tenant{}).
				Where("id = ?", tenant.ID).
				Update("store_slug", generated).Error; err != nil {
				c.JSON(http.StatusInternalServerError,
					gin.H{"error": "no se pudo asignar el enlace de tienda"})
				return
			}
			tenant.StoreSlug = &generated
		}

		c.JSON(http.StatusOK, gin.H{"data": buildSlugResponse(*tenant.StoreSlug)})
	}
}

// UpdateStoreSlug lets the tenant pick their own slug. Enforces:
//   - URL-safe regex (via services.ValidateSlugFormat)
//   - uniqueness across tenants (409 Conflict on collision)
//
// PATCH /api/v1/store/slug
// body: {"slug": "mi-tienda-123"}
func UpdateStoreSlug(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Slug string `json:"slug"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		slug := strings.ToLower(strings.TrimSpace(req.Slug))
		if err := services.ValidateSlugFormat(slug); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Collision check scoped to other tenants — re-saving the same
		// slug the tenant already owns is a no-op, not a 409.
		var existing models.Tenant
		err := db.Where("store_slug = ? AND id <> ?", slug, tenantID).
			First(&existing).Error
		if err == nil {
			c.JSON(http.StatusConflict,
				gin.H{"error": "este nombre ya está en uso, pruebe otro"})
			return
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusInternalServerError,
				gin.H{"error": "error al verificar disponibilidad del enlace"})
			return
		}

		if err := db.Model(&models.Tenant{}).
			Where("id = ?", tenantID).
			Update("store_slug", slug).Error; err != nil {
			c.JSON(http.StatusInternalServerError,
				gin.H{"error": "no se pudo actualizar el enlace de tienda"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": buildSlugResponse(slug)})
	}
}
