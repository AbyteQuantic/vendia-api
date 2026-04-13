package handlers

import (
	"net/http"
	"vendia-backend/internal/auth"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type LoginRequest struct {
	Phone    string `json:"phone"    binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login handles authentication with dual-path support:
// 1. New path: query users table → return workspaces
// 2. Legacy path: query tenants table → return single JWT
func Login(db *gorm.DB, jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req LoginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// ── New path: try users table first ─────────────────────────────
		var user models.User
		if err := db.Where("phone = ?", req.Phone).First(&user).Error; err == nil {
			if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "teléfono o contraseña incorrectos"})
				return
			}

			// Load workspaces with tenant and branch info
			var workspaces []models.UserWorkspace
			db.Preload("Tenant").Preload("Branch").
				Where("user_id = ?", user.ID).
				Find(&workspaces)

			if len(workspaces) == 0 {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "no tiene negocios asociados"})
				return
			}

			// Single workspace → auto-select, return JWT directly
			if len(workspaces) == 1 {
				ws := workspaces[0]
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
				return
			}

			// Multiple workspaces → return list for selection
			type WorkspaceOption struct {
				WorkspaceID  string `json:"workspace_id"`
				TenantID     string `json:"tenant_id"`
				TenantName   string `json:"tenant_name"`
				BranchID     string `json:"branch_id,omitempty"`
				BranchName   string `json:"branch_name,omitempty"`
				Role         string `json:"role"`
			}

			var options []WorkspaceOption
			for _, ws := range workspaces {
				opt := WorkspaceOption{
					WorkspaceID: ws.ID,
					TenantID:    ws.TenantID,
					Role:        string(ws.Role),
				}
				if ws.Tenant != nil {
					opt.TenantName = ws.Tenant.BusinessName
				}
				if ws.BranchID != nil {
					opt.BranchID = *ws.BranchID
				}
				if ws.Branch != nil {
					opt.BranchName = ws.Branch.Name
				}
				options = append(options, opt)
			}

			// Generate a temporary token (short-lived) for workspace selection
			tempToken, _ := auth.GenerateToken(user.ID, user.Phone, "", jwtSecret)

			c.JSON(http.StatusOK, gin.H{
				"workspaces": options,
				"temp_token": tempToken,
				"user_id":    user.ID,
				"user_name":  user.Name,
			})
			return
		}

		// ── Legacy path: fall back to tenants table ─────────────────────
		var tenant models.Tenant
		if err := db.Where("phone = ?", req.Phone).First(&tenant).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "teléfono o contraseña incorrectos"})
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(tenant.PasswordHash), []byte(req.Password)); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "teléfono o contraseña incorrectos"})
			return
		}

		resp, err := createTokenPair(db, tenant, jwtSecret)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al generar tokens"})
			return
		}

		c.JSON(http.StatusOK, resp)
	}
}
