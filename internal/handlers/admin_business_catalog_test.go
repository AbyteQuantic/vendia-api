// Spec: specs/041-catalogo-dinamico-modulos-tipos/spec.md
package handlers

import (
	"bytes"
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

func setupAdminCatalogDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.BusinessModule{}, &models.BusinessTypeCatalog{},
		&models.ModuleTypeRelation{}, &models.TenantModuleOverride{},
		&models.CatalogAuditLog{},
	))
	return db
}

// adminRouter registra todas las rutas F041 admin (sin SuperAdminOnly; ese
// gate se prueba aparte) con un actor inyectado en el contexto.
func adminRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.UserIDKey, "admin-1"); c.Next() })
	g := r.Group("/api/v1/admin")
	g.GET("/catalog/modules", AdminListBusinessModules(db))
	g.POST("/catalog/modules", AdminCreateBusinessModule(db))
	g.POST("/catalog/modules-reorder", AdminReorderBusinessModules(db))
	g.PATCH("/catalog/modules/:id", AdminUpdateBusinessModule(db))
	g.POST("/catalog/modules/:id/archive", AdminArchiveBusinessModule(db))
	g.PUT("/catalog/modules/:id/relations", AdminSetModuleRelations(db))
	g.GET("/catalog/business-types", AdminListBusinessTypes(db))
	g.POST("/catalog/business-types", AdminCreateBusinessType(db))
	g.PATCH("/catalog/business-types/:id", AdminUpdateBusinessType(db))
	g.POST("/catalog/business-types/:id/archive", AdminArchiveBusinessType(db))
	g.GET("/catalog/preview", AdminCatalogPreview(db))
	g.GET("/catalog/audit-logs", AdminListCatalogAuditLogs(db))
	g.GET("/tenants/:id/module-overrides", AdminListTenantOverrides(db))
	g.PUT("/tenants/:id/module-overrides/:moduleId", AdminPutTenantOverride(db))
	g.DELETE("/tenants/:id/module-overrides/:moduleId", AdminDeleteTenantOverride(db))
	return r
}

func reqJSON(r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// AC-12 — rutas admin exigen super_admin (el gate rechaza sin claims).
func TestAdminCatalog_RejectsNonSuperAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1/admin")
	g.Use(middleware.SuperAdminOnly())
	g.GET("/catalog/modules", AdminListBusinessModules(setupAdminCatalogDB(t)))

	w := reqJSON(r, http.MethodGet, "/api/v1/admin/catalog/modules", nil)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// Las rutas F041 no deben chocar en el árbol de gin (estático vs wildcard).
func TestAdminCatalog_RoutesRegisterWithoutConflict(t *testing.T) {
	db := setupAdminCatalogDB(t)
	assert.NotPanics(t, func() { _ = adminRouter(db) })
}

// AC-07 — crear y editar un módulo; el cambio se persiste y se audita.
func TestAdminCatalog_CreateUpdateModule_Audited(t *testing.T) {
	db := setupAdminCatalogDB(t)
	r := adminRouter(db)

	create := reqJSON(r, http.MethodPost, "/api/v1/admin/catalog/modules", gin.H{
		"key": "demo", "name": "Demo", "category": "vender",
	})
	require.Equal(t, http.StatusOK, create.Code)
	var created struct {
		Data models.BusinessModule `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &created))
	id := created.Data.ID
	assert.Equal(t, "demo", created.Data.Key)
	assert.True(t, created.Data.Active)

	upd := reqJSON(r, http.MethodPatch, "/api/v1/admin/catalog/modules/"+id, gin.H{
		"name": "Demo editado", "description": "nueva desc",
	})
	require.Equal(t, http.StatusOK, upd.Code)
	var reloaded models.BusinessModule
	require.NoError(t, db.First(&reloaded, "id = ?", id).Error)
	assert.Equal(t, "Demo editado", reloaded.Name)

	// Auditoría: create + update registrados.
	var logs int64
	db.Model(&models.CatalogAuditLog{}).Where("entity_id = ?", id).Count(&logs)
	assert.Equal(t, int64(2), logs)
}

// La key es inmutable: un PATCH con "key" no la cambia.
func TestAdminCatalog_KeyImmutable(t *testing.T) {
	db := setupAdminCatalogDB(t)
	r := adminRouter(db)
	create := reqJSON(r, http.MethodPost, "/api/v1/admin/catalog/modules", gin.H{
		"key": "fija", "name": "X", "category": "vender",
	})
	var created struct {
		Data models.BusinessModule `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &created))

	reqJSON(r, http.MethodPatch, "/api/v1/admin/catalog/modules/"+created.Data.ID, gin.H{"key": "otra"})
	var reloaded models.BusinessModule
	db.First(&reloaded, "id = ?", created.Data.ID)
	assert.Equal(t, "fija", reloaded.Key, "la key no cambia vía PATCH")
}

// AC-17 — archivar un tipo lo desactiva y le pone archived_at.
func TestAdminCatalog_ArchiveType(t *testing.T) {
	db := setupAdminCatalogDB(t)
	r := adminRouter(db)
	create := reqJSON(r, http.MethodPost, "/api/v1/admin/catalog/business-types", gin.H{
		"value": "panaderia", "label": "Panadería",
	})
	require.Equal(t, http.StatusOK, create.Code)
	var ct struct {
		Data models.BusinessTypeCatalog `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &ct))

	arch := reqJSON(r, http.MethodPost, "/api/v1/admin/catalog/business-types/"+ct.Data.ID+"/archive", nil)
	require.Equal(t, http.StatusNoContent, arch.Code)

	var reloaded models.BusinessTypeCatalog
	db.First(&reloaded, "id = ?", ct.Data.ID)
	assert.False(t, reloaded.Active)
	assert.NotNil(t, reloaded.ArchivedAt)
}

// AC-02/AC-03 — PUT relaciones reemplaza el set del módulo.
func TestAdminCatalog_SetRelations(t *testing.T) {
	db := setupAdminCatalogDB(t)
	r := adminRouter(db)
	create := reqJSON(r, http.MethodPost, "/api/v1/admin/catalog/modules", gin.H{
		"key": "cotiza", "name": "Cotizaciones", "category": "vender",
	})
	var cm struct {
		Data models.BusinessModule `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &cm))

	put := reqJSON(r, http.MethodPut, "/api/v1/admin/catalog/modules/"+cm.Data.ID+"/relations", []gin.H{
		{"business_type_value": "deposito_construccion", "relation_level": "suggested"},
		{"business_type_value": "restaurante", "relation_level": "implicit"},
	})
	require.Equal(t, http.StatusOK, put.Code)
	var rels int64
	db.Model(&models.ModuleTypeRelation{}).Where("module_id = ?", cm.Data.ID).Count(&rels)
	assert.Equal(t, int64(2), rels)
}

// AC-05/AC-06 — override upsert + delete por tienda.
func TestAdminCatalog_TenantOverrideUpsertDelete(t *testing.T) {
	db := setupAdminCatalogDB(t)
	r := adminRouter(db)

	put := reqJSON(r, http.MethodPut, "/api/v1/admin/tenants/T1/module-overrides/M1", gin.H{"forced_state": "active"})
	require.Equal(t, http.StatusOK, put.Code)
	var n int64
	db.Model(&models.TenantModuleOverride{}).Where("tenant_id = ? AND module_id = ?", "T1", "M1").Count(&n)
	assert.Equal(t, int64(1), n)

	// Upsert: cambia a inactive sin duplicar.
	reqJSON(r, http.MethodPut, "/api/v1/admin/tenants/T1/module-overrides/M1", gin.H{"forced_state": "inactive"})
	var ov models.TenantModuleOverride
	db.Where("tenant_id = ? AND module_id = ?", "T1", "M1").First(&ov)
	assert.Equal(t, models.OverrideInactive, ov.ForcedState)

	del := reqJSON(r, http.MethodDelete, "/api/v1/admin/tenants/T1/module-overrides/M1", nil)
	require.Equal(t, http.StatusNoContent, del.Code)
	db.Model(&models.TenantModuleOverride{}).Where("tenant_id = ?", "T1").Count(&n)
	assert.Equal(t, int64(0), n)
}

// AC-14 — preview por tipo de negocio devuelve módulos resueltos.
func TestAdminCatalog_PreviewByType(t *testing.T) {
	db := setupAdminCatalogDB(t)
	r := adminRouter(db)
	// core (siempre en grid) + opcional con relación implícita para el tipo.
	reqJSON(r, http.MethodPost, "/api/v1/admin/catalog/modules", gin.H{"key": "venta", "name": "Venta", "category": "vender"})
	cot := reqJSON(r, http.MethodPost, "/api/v1/admin/catalog/modules", gin.H{"key": "cotiza", "name": "Cotiza", "category": "vender", "capability_key": "enable_quotes"})
	var cm struct {
		Data models.BusinessModule `json:"data"`
	}
	json.Unmarshal(cot.Body.Bytes(), &cm)
	reqJSON(r, http.MethodPut, "/api/v1/admin/catalog/modules/"+cm.Data.ID+"/relations", []gin.H{
		{"business_type_value": "ferreteria", "relation_level": "implicit"},
	})

	w := reqJSON(r, http.MethodGet, "/api/v1/admin/catalog/preview?business_type=ferreteria", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []struct {
			Module models.BusinessModule `json:"module"`
			InGrid bool                  `json:"in_grid"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	inGrid := map[string]bool{}
	for _, d := range resp.Data {
		inGrid[d.Module.Key] = d.InGrid
	}
	assert.True(t, inGrid["venta"], "core siempre en grid")
	assert.True(t, inGrid["cotiza"], "implícito para ferretería → grid")
}
