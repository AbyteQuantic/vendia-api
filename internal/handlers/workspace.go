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

	return &AuthResponse{
		Token:        accessToken,
		RefreshToken: refreshStr,
		TenantID:     tenantID,
		OwnerName:    user.Name,
		BusinessName: businessName,
	}, nil
}

// SelectWorkspace selects a workspace and returns the final JWT.
// POST /api/v1/auth/select-workspace
func SelectWorkspace(db *gorm.DB, jwtSecret string) gin.HandlerFunc {
	type Request struct {
		WorkspaceID string `json:"workspace_id" binding:"required"`
	}

	return func(c *gin.Context) {
		// The user authenticated with temp_token, so tenant_id = user_id in temp token
		userID := middleware.GetTenantID(c) // temp token stores user_id as tenant_id

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
