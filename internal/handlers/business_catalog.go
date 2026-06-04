// Spec: specs/041-catalogo-dinamico-modulos-tipos/spec.md
//
// Lectura del catálogo dinámico de módulos/tipos para la app del tendero
// (F041). Devuelve los módulos + tipos + relaciones globales (no archivados)
// y los overrides de ESTA tienda, con un ETag para que la app evite
// re-descargar cuando nada cambió (D3 — pull al abrir + version/etag). La
// resolución de qué se ve la hace la app (offline-first) con estos datos +
// sus banderas enable_*. (Archivo aparte del catálogo de PRODUCTOS catalog.go.)

package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
)

// businessCatalogETag deriva un identificador estable del estado del
// catálogo + overrides de la tienda: cambia si se edita algo (max
// updated_at) o si se agrega/archiva una fila (los conteos). Incluye los
// overrides del propio tenant, así dos tiendas no comparten ETag.
func businessCatalogETag(
	modules []models.BusinessModule,
	types []models.BusinessTypeCatalog,
	relations []models.ModuleTypeRelation,
	overrides []models.TenantModuleOverride,
) string {
	var latest time.Time
	bump := func(t time.Time) {
		if t.After(latest) {
			latest = t
		}
	}
	for _, m := range modules {
		bump(m.UpdatedAt)
	}
	for _, t := range types {
		bump(t.UpdatedAt)
	}
	for _, r := range relations {
		bump(r.UpdatedAt)
	}
	for _, o := range overrides {
		bump(o.UpdatedAt)
	}
	raw := fmt.Sprintf("%d-%d-%d-%d-%d",
		len(modules), len(types), len(relations), len(overrides), latest.UnixNano())
	sum := sha256.Sum256([]byte(raw))
	return `"` + hex.EncodeToString(sum[:12]) + `"`
}

// GetBusinessCatalog — GET /api/v1/catalog (JWT tenant, solo lectura).
func GetBusinessCatalog(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var modules []models.BusinessModule
		if err := db.Where("archived_at IS NULL").
			Order("sort_order asc").Find(&modules).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "No se pudo cargar el catálogo"})
			return
		}
		var types []models.BusinessTypeCatalog
		db.Where("archived_at IS NULL").Order("sort_order asc").Find(&types)
		var relations []models.ModuleTypeRelation
		db.Find(&relations)

		// Overrides SOLO de esta tienda (aislamiento multi-tenant — Art. III).
		var overrides []models.TenantModuleOverride
		if tenantID != "" {
			db.Where("tenant_id = ?", tenantID).Find(&overrides)
		}

		etag := businessCatalogETag(modules, types, relations, overrides)
		if match := c.GetHeader("If-None-Match"); match != "" && match == etag {
			c.Status(http.StatusNotModified)
			return
		}

		c.Header("ETag", etag)
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"modules":   modules,
			"types":     types,
			"relations": relations,
			"overrides": overrides,
			"version":   etag,
		}})
	}
}
