// Spec: specs/041-catalogo-dinamico-modulos-tipos/spec.md
//
// Gestión del catálogo dinámico desde el admin (F041 Fase 2). Todos los
// endpoints van bajo /api/v1/admin (SuperAdminOnly). Cada escritura queda
// en el log de auditoría (D9). La `key`/`value` estable es inmutable: los
// PATCH la ignoran. Archivar (no borrar) conserva el dato (D6).

package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"vendia-backend/internal/auth"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"
)

// ── helpers ──────────────────────────────────────────────────────────────

func catalogActor(c *gin.Context) (string, string) {
	id := middleware.GetUserID(c)
	name := ""
	if v, ok := c.Get(middleware.ClaimsKey); ok {
		if cl, ok := v.(*auth.Claims); ok {
			name = cl.Phone
			if id == "" {
				id = cl.TenantID
			}
		}
	}
	return id, name
}

var validCategories = map[string]bool{
	models.CategoryVender: true, models.CategoryInventario: true,
	models.CategoryClientes: true, models.CategoryMiNegocio: true,
}
var validRenderTypes = map[string]bool{
	models.RenderNative: true, models.RenderWebview: true, models.RenderPlaceholder: true,
}
var validRelationLevels = map[string]bool{
	models.RelationImplicit: true, models.RelationSuggested: true, models.RelationAvailable: true,
}

// ── Módulos ────────────────────────────────────────────────────────────

// AdminListBusinessModules — GET /admin/catalog/modules (incluye archivados).
func AdminListBusinessModules(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var modules []models.BusinessModule
		db.Order("sort_order asc").Find(&modules)
		c.JSON(http.StatusOK, gin.H{"data": modules})
	}
}

// AdminCreateBusinessModule — POST /admin/catalog/modules.
func AdminCreateBusinessModule(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Key             string  `json:"key"`
			Name            string  `json:"name"`
			Description     string  `json:"description"`
			IconKey         string  `json:"icon_key"`
			Color           string  `json:"color"`
			Category        string  `json:"category"`
			RenderType      string  `json:"render_type"`
			NativeScreenKey *string `json:"native_screen_key"`
			WebviewURL      *string `json:"webview_url"`
			CapabilityKey   *string `json:"capability_key"`
			RequiresPro     bool    `json:"requires_pro"`
			SortOrder       int     `json:"sort_order"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Datos inválidos"})
			return
		}
		if req.Key == "" || req.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "La clave y el nombre son obligatorios"})
			return
		}
		if !validCategories[req.Category] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Categoría inválida"})
			return
		}
		if req.RenderType == "" {
			req.RenderType = models.RenderNative
		}
		if !validRenderTypes[req.RenderType] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Tipo de render inválido"})
			return
		}
		var dup int64
		db.Model(&models.BusinessModule{}).Where("key = ?", req.Key).Count(&dup)
		if dup > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "Ya existe un módulo con esa clave"})
			return
		}
		actorID, actorName := catalogActor(c)
		m := models.BusinessModule{
			Key: req.Key, Name: req.Name, Description: req.Description,
			IconKey: req.IconKey, Color: req.Color, Category: req.Category,
			RenderType: req.RenderType, NativeScreenKey: req.NativeScreenKey,
			WebviewURL: req.WebviewURL, CapabilityKey: req.CapabilityKey,
			RequiresPro: req.RequiresPro, Active: true, SortOrder: req.SortOrder,
			CreatedBy: actorID, UpdatedBy: actorID,
		}
		if err := db.Create(&m).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "No se pudo crear el módulo"})
			return
		}
		services.LogCatalogChange(db, actorID, actorName, "module", m.ID, services.AuditCreate, nil, m)
		c.JSON(http.StatusOK, gin.H{"data": m})
	}
}

// AdminUpdateBusinessModule — PATCH /admin/catalog/modules/:id (parcial).
// La `key` es inmutable: no se acepta en el cuerpo.
func AdminUpdateBusinessModule(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var m models.BusinessModule
		if err := db.First(&m, "id = ?", c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Módulo no encontrado"})
			return
		}
		before := m
		var req struct {
			Name            *string `json:"name"`
			Description     *string `json:"description"`
			IconKey         *string `json:"icon_key"`
			Color           *string `json:"color"`
			Category        *string `json:"category"`
			RenderType      *string `json:"render_type"`
			NativeScreenKey *string `json:"native_screen_key"`
			WebviewURL      *string `json:"webview_url"`
			CapabilityKey   *string `json:"capability_key"`
			RequiresPro     *bool   `json:"requires_pro"`
			Active          *bool   `json:"active"`
			SortOrder       *int    `json:"sort_order"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Datos inválidos"})
			return
		}
		if req.Name != nil {
			m.Name = *req.Name
		}
		if req.Description != nil {
			m.Description = *req.Description
		}
		if req.IconKey != nil {
			m.IconKey = *req.IconKey
		}
		if req.Color != nil {
			m.Color = *req.Color
		}
		if req.Category != nil {
			if !validCategories[*req.Category] {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Categoría inválida"})
				return
			}
			m.Category = *req.Category
		}
		if req.RenderType != nil {
			if !validRenderTypes[*req.RenderType] {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Tipo de render inválido"})
				return
			}
			m.RenderType = *req.RenderType
		}
		if req.NativeScreenKey != nil {
			m.NativeScreenKey = req.NativeScreenKey
		}
		if req.WebviewURL != nil {
			m.WebviewURL = req.WebviewURL
		}
		if req.CapabilityKey != nil {
			m.CapabilityKey = req.CapabilityKey
		}
		if req.RequiresPro != nil {
			m.RequiresPro = *req.RequiresPro
		}
		if req.Active != nil {
			m.Active = *req.Active
		}
		if req.SortOrder != nil {
			m.SortOrder = *req.SortOrder
		}
		actorID, actorName := catalogActor(c)
		m.UpdatedBy = actorID
		if err := db.Save(&m).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "No se pudo actualizar el módulo"})
			return
		}
		services.LogCatalogChange(db, actorID, actorName, "module", m.ID, services.AuditUpdate, before, m)
		c.JSON(http.StatusOK, gin.H{"data": m})
	}
}

// AdminArchiveBusinessModule — POST /admin/catalog/modules/:id/archive.
func AdminArchiveBusinessModule(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var m models.BusinessModule
		if err := db.First(&m, "id = ?", c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Módulo no encontrado"})
			return
		}
		before := m
		now := time.Now()
		m.ArchivedAt = &now
		m.Active = false
		actorID, actorName := catalogActor(c)
		m.UpdatedBy = actorID
		if err := db.Save(&m).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "No se pudo archivar el módulo"})
			return
		}
		services.LogCatalogChange(db, actorID, actorName, "module", m.ID, services.AuditArchive, before, m)
		c.Status(http.StatusNoContent)
	}
}

// AdminReorderBusinessModules — POST /admin/catalog/modules/reorder.
func AdminReorderBusinessModules(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req []struct {
			ID        string `json:"id"`
			SortOrder int    `json:"sort_order"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Datos inválidos"})
			return
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			for _, it := range req {
				if err := tx.Model(&models.BusinessModule{}).
					Where("id = ?", it.ID).Update("sort_order", it.SortOrder).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "No se pudo reordenar"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// ── Tipos de negocio ─────────────────────────────────────────────────────

// AdminListBusinessTypes — GET /admin/catalog/business-types.
func AdminListBusinessTypes(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var types []models.BusinessTypeCatalog
		db.Order("sort_order asc").Find(&types)
		c.JSON(http.StatusOK, gin.H{"data": types})
	}
}

// AdminCreateBusinessType — POST /admin/catalog/business-types.
func AdminCreateBusinessType(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Value     string `json:"value"`
			Label     string `json:"label"`
			IconKey   string `json:"icon_key"`
			SortOrder int    `json:"sort_order"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Value == "" || req.Label == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "El valor y la etiqueta son obligatorios"})
			return
		}
		var dup int64
		db.Model(&models.BusinessTypeCatalog{}).Where("value = ?", req.Value).Count(&dup)
		if dup > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "Ya existe un tipo con ese valor"})
			return
		}
		actorID, actorName := catalogActor(c)
		t := models.BusinessTypeCatalog{
			Value: req.Value, Label: req.Label, IconKey: req.IconKey,
			Active: true, SortOrder: req.SortOrder, CreatedBy: actorID, UpdatedBy: actorID,
		}
		if err := db.Create(&t).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "No se pudo crear el tipo"})
			return
		}
		services.LogCatalogChange(db, actorID, actorName, "business_type", t.ID, services.AuditCreate, nil, t)
		c.JSON(http.StatusOK, gin.H{"data": t})
	}
}

// AdminUpdateBusinessType — PATCH /admin/catalog/business-types/:id.
func AdminUpdateBusinessType(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var t models.BusinessTypeCatalog
		if err := db.First(&t, "id = ?", c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Tipo no encontrado"})
			return
		}
		before := t
		var req struct {
			Label     *string `json:"label"`
			IconKey   *string `json:"icon_key"`
			Active    *bool   `json:"active"`
			SortOrder *int    `json:"sort_order"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Datos inválidos"})
			return
		}
		if req.Label != nil {
			t.Label = *req.Label
		}
		if req.IconKey != nil {
			t.IconKey = *req.IconKey
		}
		if req.Active != nil {
			t.Active = *req.Active
		}
		if req.SortOrder != nil {
			t.SortOrder = *req.SortOrder
		}
		actorID, actorName := catalogActor(c)
		t.UpdatedBy = actorID
		if err := db.Save(&t).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "No se pudo actualizar el tipo"})
			return
		}
		services.LogCatalogChange(db, actorID, actorName, "business_type", t.ID, services.AuditUpdate, before, t)
		c.JSON(http.StatusOK, gin.H{"data": t})
	}
}

// AdminArchiveBusinessType — POST /admin/catalog/business-types/:id/archive.
// D6: archivar (no borrar). Las tiendas que ya tienen el valor lo conservan;
// solo se oculta para nuevas selecciones.
func AdminArchiveBusinessType(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var t models.BusinessTypeCatalog
		if err := db.First(&t, "id = ?", c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Tipo no encontrado"})
			return
		}
		before := t
		now := time.Now()
		t.ArchivedAt = &now
		t.Active = false
		actorID, actorName := catalogActor(c)
		t.UpdatedBy = actorID
		if err := db.Save(&t).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "No se pudo archivar el tipo"})
			return
		}
		services.LogCatalogChange(db, actorID, actorName, "business_type", t.ID, services.AuditArchive, before, t)
		c.Status(http.StatusNoContent)
	}
}

// ── Relaciones módulo ↔ tipo ─────────────────────────────────────────────

// AdminSetModuleRelations — PUT /admin/catalog/modules/:id/relations.
// Reemplaza TODAS las relaciones del módulo. Cambiar el nivel NO toca las
// banderas enable_* del tenant, así que la elección previa del tendero se
// preserva por construcción (D7).
func AdminSetModuleRelations(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		moduleID := c.Param("id")
		var m models.BusinessModule
		if err := db.First(&m, "id = ?", moduleID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Módulo no encontrado"})
			return
		}
		var req []struct {
			BusinessTypeValue string `json:"business_type_value"`
			RelationLevel     string `json:"relation_level"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Datos inválidos"})
			return
		}
		for _, r := range req {
			if !validRelationLevels[r.RelationLevel] {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Nivel de relación inválido"})
				return
			}
		}
		var before []models.ModuleTypeRelation
		db.Where("module_id = ?", moduleID).Find(&before)

		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("module_id = ?", moduleID).Delete(&models.ModuleTypeRelation{}).Error; err != nil {
				return err
			}
			for _, r := range req {
				rel := models.ModuleTypeRelation{
					ModuleID: moduleID, BusinessTypeValue: r.BusinessTypeValue, RelationLevel: r.RelationLevel,
				}
				if err := tx.Create(&rel).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "No se pudieron guardar las relaciones"})
			return
		}
		var after []models.ModuleTypeRelation
		db.Where("module_id = ?", moduleID).Find(&after)
		actorID, actorName := catalogActor(c)
		services.LogCatalogChange(db, actorID, actorName, "module_relations", moduleID, services.AuditRelate, before, after)
		c.JSON(http.StatusOK, gin.H{"data": after})
	}
}

// ── Overrides por tienda ─────────────────────────────────────────────────

// AdminListTenantOverrides — GET /admin/tenants/:id/module-overrides.
func AdminListTenantOverrides(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var overrides []models.TenantModuleOverride
		db.Where("tenant_id = ?", c.Param("id")).Find(&overrides)
		c.JSON(http.StatusOK, gin.H{"data": overrides})
	}
}

// AdminPutTenantOverride — PUT /admin/tenants/:id/module-overrides/:moduleId.
func AdminPutTenantOverride(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := c.Param("id")
		moduleID := c.Param("moduleId")
		var req struct {
			ForcedState string `json:"forced_state"`
		}
		if err := c.ShouldBindJSON(&req); err != nil ||
			(req.ForcedState != models.OverrideActive && req.ForcedState != models.OverrideInactive) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Estado forzado inválido (active|inactive)"})
			return
		}
		var ov models.TenantModuleOverride
		err := db.Where("tenant_id = ? AND module_id = ?", tenantID, moduleID).First(&ov).Error
		actorID, actorName := catalogActor(c)
		if err == gorm.ErrRecordNotFound {
			ov = models.TenantModuleOverride{
				TenantID: tenantID, ModuleID: moduleID, ForcedState: req.ForcedState, CreatedBy: actorID,
			}
			if e := db.Create(&ov).Error; e != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "No se pudo crear el override"})
				return
			}
			services.LogCatalogChange(db, actorID, actorName, "override", ov.ID, services.AuditCreate, nil, ov)
		} else {
			before := ov
			ov.ForcedState = req.ForcedState
			if e := db.Save(&ov).Error; e != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "No se pudo actualizar el override"})
				return
			}
			services.LogCatalogChange(db, actorID, actorName, "override", ov.ID, services.AuditUpdate, before, ov)
		}
		c.JSON(http.StatusOK, gin.H{"data": ov})
	}
}

// AdminDeleteTenantOverride — DELETE /admin/tenants/:id/module-overrides/:moduleId.
func AdminDeleteTenantOverride(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := c.Param("id")
		moduleID := c.Param("moduleId")
		var ov models.TenantModuleOverride
		if err := db.Where("tenant_id = ? AND module_id = ?", tenantID, moduleID).First(&ov).Error; err != nil {
			c.Status(http.StatusNoContent) // ya no existe → idempotente
			return
		}
		db.Delete(&ov)
		actorID, actorName := catalogActor(c)
		services.LogCatalogChange(db, actorID, actorName, "override", ov.ID, services.AuditDelete, ov, nil)
		c.Status(http.StatusNoContent)
	}
}

// ── Previsualización ─────────────────────────────────────────────────────

// AdminCatalogPreview — GET /admin/catalog/preview?business_type=…|tenant_id=…
// Devuelve los módulos resueltos para un tipo (tienda nueva) o una tienda.
func AdminCatalogPreview(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var modules []models.BusinessModule
		db.Where("archived_at IS NULL").Order("sort_order asc").Find(&modules)
		var relations []models.ModuleTypeRelation
		db.Find(&relations)

		in := services.ResolveInput{Modules: modules, Relations: relations, IsPro: true}

		if tid := c.Query("tenant_id"); tid != "" {
			var t models.Tenant
			if err := db.First(&t, "id = ?", tid).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "Tienda no encontrada"})
				return
			}
			in.TenantTypes = t.BusinessTypes
			in.CapabilityState = tenantCapabilityState(t)
			var overrides []models.TenantModuleOverride
			db.Where("tenant_id = ?", tid).Find(&overrides)
			in.Overrides = overrides
		} else if bt := c.Query("business_type"); bt != "" {
			in.TenantTypes = []string{bt}
			in.CapabilityState = map[string]bool{}
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Indique business_type o tenant_id"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": services.ResolveModules(in)})
	}
}

// tenantCapabilityState mapea las banderas enable_* del tenant al map que
// usa el resolver (capability_key → activado).
func tenantCapabilityState(t models.Tenant) map[string]bool {
	return map[string]bool{
		"enable_quotes":              t.EnableQuotes,
		"enable_supplies":            t.EnableSupplies,
		"enable_recipes":             t.EnableRecipes,
		"enable_purchase_orders":     t.EnablePurchaseOrders,
		"enable_furniture_jobs":      t.EnableFurnitureJobs,
		"enable_customer_management": t.EnableCustomerManagement,
		"enable_promotions":          t.EnablePromotions,
		"enable_marketing_hub":       t.EnableMarketingHub,
		"enable_price_tiers":         t.EnablePriceTiers,
	}
}

// ── Auditoría ──────────────────────────────────────────────────────────

// AdminListCatalogAuditLogs — GET /admin/catalog/audit-logs.
func AdminListCatalogAuditLogs(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var logs []models.CatalogAuditLog
		q := db.Order("created_at desc").Limit(200)
		if et := c.Query("entity"); et != "" {
			q = q.Where("entity_type = ?", et)
		}
		q.Find(&logs)
		c.JSON(http.StatusOK, gin.H{"data": logs})
	}
}
