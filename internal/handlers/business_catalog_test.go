// Spec: specs/041-catalogo-dinamico-modulos-tipos/spec.md
package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
)

func setupCatalogHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.BusinessModule{},
		&models.BusinessTypeCatalog{},
		&models.ModuleTypeRelation{},
		&models.TenantModuleOverride{},
	))
	// 2 módulos (uno archivado, que NO debe aparecer) + 1 tipo.
	require.NoError(t, db.Create(&models.BusinessModule{
		BaseModel: models.BaseModel{ID: "m-quotes"}, Key: "cotizaciones",
		Name: "Cotizaciones", Category: models.CategoryVender, Active: true,
	}).Error)
	archived := models.BusinessModule{
		BaseModel: models.BaseModel{ID: "m-old"}, Key: "viejo",
		Name: "Viejo", Category: models.CategoryVender, Active: true,
	}
	require.NoError(t, db.Create(&archived).Error)
	require.NoError(t, db.Model(&archived).Update("archived_at", gorm.Expr("CURRENT_TIMESTAMP")).Error)

	require.NoError(t, db.Create(&models.BusinessTypeCatalog{
		BaseModel: models.BaseModel{ID: "t-1"}, Value: "tienda_barrio",
		Label: "Tienda de Barrio", Active: true,
	}).Error)
	// Override solo para la tienda A.
	require.NoError(t, db.Create(&models.TenantModuleOverride{
		BaseModel: models.BaseModel{ID: "ov-1"}, TenantID: "tienda-A",
		ModuleID: "m-quotes", ForcedState: models.OverrideActive,
	}).Error)
	return db
}

// doCatalogGet usa un router real + ServeHTTP para que gin finalice la
// respuesta (un `c.Status(304)` solo se vuelca al recorder vía ServeHTTP;
// invocar el handler directo dejaría el código en 200 sin flush).
func doCatalogGet(db *gorm.DB, tenantID, ifNoneMatch string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.GET("/api/v1/catalog", GetBusinessCatalog(db))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	r.ServeHTTP(w, req)
	return w
}

func TestGetBusinessCatalog_ReturnsActiveAndTenantOverrides(t *testing.T) {
	db := setupCatalogHandlerDB(t)

	w := doCatalogGet(db, "tienda-A", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotEmpty(t, w.Header().Get("ETag"))

	var resp struct {
		Data struct {
			Modules   []models.BusinessModule       `json:"modules"`
			Overrides []models.TenantModuleOverride `json:"overrides"`
			Version   string                        `json:"version"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// Solo el módulo no archivado.
	assert.Len(t, resp.Data.Modules, 1)
	assert.Equal(t, "cotizaciones", resp.Data.Modules[0].Key)
	// La tienda A ve su override.
	assert.Len(t, resp.Data.Overrides, 1)
	assert.NotEmpty(t, resp.Data.Version)
}

func TestGetBusinessCatalog_ETag304(t *testing.T) {
	db := setupCatalogHandlerDB(t)

	first := doCatalogGet(db, "tienda-A", "")
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)

	// Mismo ETag → 304 sin cuerpo.
	second := doCatalogGet(db, "tienda-A", etag)
	assert.Equal(t, http.StatusNotModified, second.Code)
	assert.Empty(t, second.Body.String())
}

func TestGetBusinessCatalog_OverrideIsolation(t *testing.T) {
	db := setupCatalogHandlerDB(t)

	// La tienda B NO ve el override de la tienda A (Art. III).
	w := doCatalogGet(db, "tienda-B", "")
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Overrides []models.TenantModuleOverride `json:"overrides"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Data.Overrides, "una tienda no recibe overrides de otra")
}
