package handlers

import (
	"errors"
	"net/http"
	"strings"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ── Types ────────────────────────────────────────────────────────────────────

type createBranchRequest struct {
	Name    string `json:"name"    binding:"required"`
	Address string `json:"address"`
}

type updateBranchRequest struct {
	Name     *string `json:"name"`
	Address  *string `json:"address"`
	IsActive *bool   `json:"is_active"`
}

// ── List ─────────────────────────────────────────────────────────────────────

// ListBranches returns every active sede owned by the authenticated
// tenant. Soft-deleted rows are excluded by GORM's default scope; we
// also filter is_active=false out at the query layer so archived
// sedes don't clutter the picker in the Flutter app.
func ListBranches(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if tenantID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "sesión requerida"})
			return
		}

		var branches []models.Branch
		err := db.Where("tenant_id = ? AND is_active = ?", tenantID, true).
			Order("created_at ASC").
			Find(&branches).Error
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "error al obtener sucursales",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": branches})
	}
}

// ── Create ───────────────────────────────────────────────────────────────────

// CreateBranch persists a new sede for the caller's tenant. The
// endpoint is mounted behind PremiumAuth — adding a second sede is
// one of the paywalled PRO features, so FREE/PAST_DUE tenants get
// the 403 premium_expired response the Flutter client already knows
// how to render as an upsell sheet.
func CreateBranch(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if tenantID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "sesión requerida"})
			return
		}

		var req createBranchRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "nombre de la sede es obligatorio",
			})
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "nombre no puede estar vacío",
			})
			return
		}

		branch := models.Branch{
			TenantID: tenantID,
			Name:     name,
			Address:  strings.TrimSpace(req.Address),
			IsActive: true,
		}
		if err := db.Create(&branch).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo crear la sede",
			})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": branch})
	}
}

// ── Update ───────────────────────────────────────────────────────────────────

// UpdateBranch supports partial edits: only the fields present in
// the JSON body are modified. Tenants can only edit their own sedes
// — the WHERE clause includes tenant_id so a crafted request from
// tenant A against a sede belonging to tenant B returns 404 (not
// 403, to avoid leaking that the row exists).
func UpdateBranch(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if tenantID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "sesión requerida"})
			return
		}
		branchID := c.Param("id")

		var req updateBranchRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "payload inválido"})
			return
		}

		updates := map[string]any{}
		if req.Name != nil {
			trimmed := strings.TrimSpace(*req.Name)
			if trimmed == "" {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "nombre no puede ser vacío",
				})
				return
			}
			updates["name"] = trimmed
		}
		if req.Address != nil {
			updates["address"] = strings.TrimSpace(*req.Address)
		}
		if req.IsActive != nil {
			updates["is_active"] = *req.IsActive
		}
		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "ningún campo para actualizar",
			})
			return
		}

		result := db.Model(&models.Branch{}).
			Where("id = ? AND tenant_id = ?", branchID, tenantID).
			Updates(updates)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo actualizar la sede",
			})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "sede no encontrada"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "sede actualizada"})
	}
}

// ── Delete (soft) ────────────────────────────────────────────────────────────

// DeleteBranch is a soft delete plus a safety guard: a tenant must
// always keep at least one active sede, otherwise employees /
// inventory / sales rows lose their foreign scope. Blocking the
// delete at the application layer is cheaper than a DB trigger and
// gives a readable Spanish error.
func DeleteBranch(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if tenantID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "sesión requerida"})
			return
		}
		branchID := c.Param("id")

		var count int64
		err := db.Model(&models.Branch{}).
			Where("tenant_id = ? AND is_active = ?", tenantID, true).
			Count(&count).Error
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "error al verificar sucursales",
			})
			return
		}
		if count <= 1 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "no puede eliminar la única sede activa del negocio",
				"error_code": "last_branch",
			})
			return
		}

		// Find first — so we can distinguish "not found" vs delete-error.
		var branch models.Branch
		err = db.Where("id = ? AND tenant_id = ?", branchID, tenantID).
			First(&branch).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "sede no encontrada"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error"})
			return
		}

		if err := db.Delete(&branch).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo eliminar la sede",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "sede eliminada"})
	}
}
