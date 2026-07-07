package handlers

import (
	"net/http"
	"time"
	"vendia-backend/internal/auth"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// createWorkspaceTokenPair generates JWT + refresh token for a specific workspace.
// The tenant row is re-read so the response always carries the latest
// feature_flags — even if the workspace preload was built from a stale
// join. A miss on the tenant is non-fatal: flags default to all-off.
func createWorkspaceTokenPair(db *gorm.DB, user models.User, tenantID, branchID, businessName, role, jwtSecret string) (*AuthResponse, error) {
	accessToken, err := auth.GenerateWorkspaceToken(
		user.ID, tenantID, branchID, user.Phone, businessName, role, jwtSecret,
	)
	if err != nil {
		return nil, err
	}

	refreshStr, err := auth.GenerateRefreshToken()
	if err != nil {
		return nil, err
	}

	rt := models.RefreshToken{
		TenantID:  tenantID,
		UserID:    &user.ID,
		Token:     refreshStr,
		ExpiresAt: time.Now().Add(auth.RefreshTokenDuration),
	}
	if err := db.Create(&rt).Error; err != nil {
		return nil, err
	}

	// Spec 051 — además del JSONB feature_flags, cargamos las columnas de
	// capacidad top-level para emitirlas en la respuesta. Sin ellas el
	// dashboard re-clasificaba una capacidad ACTIVA como "Descubre más
	// opciones" en cada login (eran columnas omitidas del Select y del payload).
	var tenant models.Tenant
	_ = db.Select(
		"id", "business_types", "feature_flags", "credit_label_mode",
		"enable_recipes", "enable_marketing_hub", "enable_quotes",
		"enable_promotions", "enable_customer_management", "enable_supplies",
		"enable_furniture_jobs", "enable_purchase_orders", "enable_price_tiers",
		"terms_accepted_version", // Spec 098 — para terms_acceptance_required
	).First(&tenant, "id = ?", tenantID).Error

	resp := &AuthResponse{
		Token:           accessToken,
		AccessToken:     accessToken,
		RefreshToken:    refreshStr,
		TenantID:        tenantID,
		OwnerName:       user.Name,
		BusinessName:    businessName,
		BusinessTypes:   tenant.BusinessTypes,
		FeatureFlags:    tenant.FeatureFlags,
		CreditLabelMode: tenant.CreditLabelMode,
		Role:            role,
		BranchID:        branchID,
		UserID:          user.ID,
	}
	applyCapabilityFlags(resp, tenant)
	return resp, nil
}

// SelectWorkspace exchanges the temp_token + a per-workspace password
// for the final access+refresh JWT pair.
//
// POST /api/v1/auth/select-workspace
//
// The password gate is what enforces the credential boundary across
// tenants: a user holding multiple workspaces (e.g. owner of Tienda A
// + cashier at Tienda B) proves identity once at /login but must
// re-enter the password specific to the chosen tenant before getting
// a JWT for it. Tienda A's password cannot mint a JWT for Tienda B.
func SelectWorkspace(db *gorm.DB, jwtSecret string) gin.HandlerFunc {
	type Request struct {
		WorkspaceID string `json:"workspace_id" binding:"required"`
		Password    string `json:"password"     binding:"required"`
	}

	return func(c *gin.Context) {
		// Temp token stores user_id as tenant_id (see Login's
		// auth.GenerateToken call in respondWithSelector).
		userID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var ws models.UserWorkspace
		if err := db.Preload("Tenant").
			Where("id = ? AND user_id = ?", req.WorkspaceID, userID).
			First(&ws).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "workspace no encontrado"})
			return
		}

		var user models.User
		if err := db.First(&user, "id = ?", userID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "usuario no encontrado"})
			return
		}

		if !verifyPasswordForWorkspace(db, user, ws, []byte(req.Password)) {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error":      "esa clave no abre este negocio",
				"error_code": "workspace_password_mismatch",
			})
			return
		}

		branchID := ""
		if ws.BranchID != nil {
			branchID = *ws.BranchID
		}
		businessName := ""
		if ws.Tenant != nil {
			businessName = ws.Tenant.BusinessName
		}

		resp, err := createWorkspaceTokenPair(db, user, ws.TenantID, branchID, businessName, string(ws.Role), jwtSecret)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al generar tokens"})
			return
		}

		c.JSON(http.StatusOK, resp)
	}
}

// ListWorkspaces returns the workspaces for the authenticated user.
// GET /api/v1/auth/workspaces
func ListWorkspaces(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := middleware.GetUserID(c)
		if userID == "" {
			// Legacy token — use tenant_id to find the single workspace
			tenantID := middleware.GetTenantID(c)
			var tenant models.Tenant
			if err := db.First(&tenant, "id = ?", tenantID).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"data": []gin.H{{
					"tenant_id":   tenant.ID,
					"tenant_name": tenant.BusinessName,
					"role":        "owner",
				}},
			})
			return
		}

		var workspaces []models.UserWorkspace
		db.Preload("Tenant").Preload("Branch").
			Where("user_id = ?", userID).
			Find(&workspaces)

		type WS struct {
			WorkspaceID string `json:"workspace_id"`
			TenantID    string `json:"tenant_id"`
			TenantName  string `json:"tenant_name"`
			BranchID    string `json:"branch_id,omitempty"`
			BranchName  string `json:"branch_name,omitempty"`
			Role        string `json:"role"`
		}

		var result []WS
		for _, ws := range workspaces {
			item := WS{
				WorkspaceID: ws.ID,
				TenantID:    ws.TenantID,
				Role:        string(ws.Role),
			}
			if ws.Tenant != nil {
				item.TenantName = ws.Tenant.BusinessName
			}
			if ws.BranchID != nil {
				item.BranchID = *ws.BranchID
			}
			if ws.Branch != nil {
				item.BranchName = ws.Branch.Name
			}
			result = append(result, item)
		}

		c.JSON(http.StatusOK, gin.H{"data": result})
	}
}
