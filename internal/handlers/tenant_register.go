package handlers

import (
	"fmt"
	"net/http"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type TenantRegisterRequest struct {
	Owner     OwnerInput      `json:"owner"     binding:"required"`
	Business  BusinessInput   `json:"business"  binding:"required"`
	Config    ConfigInput     `json:"config"    binding:"required"`
	Employees []EmployeeInput `json:"employees"`
}

type OwnerInput struct {
	Name     string `json:"name"     binding:"required"`
	Phone    string `json:"phone"    binding:"required,min=7,max=15"`
	Password string `json:"password" binding:"required,min=4"`
}

type BusinessInput struct {
	Name        string   `json:"name"         binding:"required"`
	Type        string   `json:"type"`
	Types       []string `json:"types"`
	RazonSocial string   `json:"razon_social"`
	NIT         string   `json:"nit"`
	Address     string   `json:"address"`
}

type ConfigInput struct {
	SaleTypes    []string `json:"sale_types"    binding:"required,min=1"`
	HasShowcases bool     `json:"has_showcases"`
	HasTables    bool     `json:"has_tables"`
}

type EmployeeInput struct {
	Name     string              `json:"name"     binding:"required"`
	Phone    string              `json:"phone"`
	Role     models.EmployeeRole `json:"role"     binding:"required"`
	Password string              `json:"password" binding:"required,min=4"`
}

func TenantRegister(db *gorm.DB, jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req TenantRegisterRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Check both users and tenants tables for existing phone
		var existingUser models.User
		userExists := db.Where("phone = ?", req.Owner.Phone).First(&existingUser).Error == nil
		var existingTenant models.Tenant
		tenantExists := db.Where("phone = ?", req.Owner.Phone).First(&existingTenant).Error == nil

		if userExists || tenantExists {
			c.JSON(http.StatusConflict, gin.H{"error": "ese número ya está registrado"})
			return
		}

		businessTypes, validationErr := validateBusinessTypes(resolveBusinessTypes(req.Business))
		if validationErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": validationErr.Error()})
			return
		}

		ownerHash, err := bcrypt.GenerateFromPassword([]byte(req.Owner.Password), 12)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al procesar contraseña"})
			return
		}

		flags := models.DefaultFeatureFlags(businessTypes, req.Config.HasTables)

		var tenant models.Tenant
		var user models.User

		txErr := db.Transaction(func(tx *gorm.DB) error {
			// 1. Create User (global identity)
			user = models.User{
				Phone:        req.Owner.Phone,
				Name:         req.Owner.Name,
				PasswordHash: string(ownerHash),
			}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}

			// 2. Create Tenant (business) — dual-write phone/password for legacy compat
			tenant = models.Tenant{
				OwnerName:     req.Owner.Name,
				Phone:         req.Owner.Phone,
				PasswordHash:  string(ownerHash),
				BusinessName:  req.Business.Name,
				BusinessTypes: businessTypes,
				FeatureFlags:  flags,
				RazonSocial:   req.Business.RazonSocial,
				NIT:           req.Business.NIT,
				Address:       req.Business.Address,
				SaleTypes:     req.Config.SaleTypes,
				HasShowcases:  req.Config.HasShowcases,
				HasTables:     req.Config.HasTables || flags.EnableTables,
			}
			if err := tx.Create(&tenant).Error; err != nil {
				return err
			}

			// 3. Create default Branch
			branch := models.Branch{
				TenantID: tenant.ID,
				Name:     "Principal",
				Address:  req.Business.Address,
				IsActive: true,
			}
			if err := tx.Create(&branch).Error; err != nil {
				return err
			}

			// 4. Create UserWorkspace (owner link)
			ws := models.UserWorkspace{
				UserID:    user.ID,
				TenantID:  tenant.ID,
				BranchID:  &branch.ID,
				Role:      models.RoleOwner,
				IsDefault: true,
			}
			if err := tx.Create(&ws).Error; err != nil {
				return err
			}

			// 5. Create Employee(s) — each one is scoped to the
			//    "Sede Principal" we just created. Multi-branch
			//    tenants (PRO) reassign employees later through the
			//    /api/v1/store/employees PATCH endpoint.
			branchIDPtr := &branch.ID
			if len(req.Employees) == 0 {
				defaultCashier := models.Employee{
					TenantID:     tenant.ID,
					BranchID:     branchIDPtr,
					Name:         req.Owner.Name,
					Phone:        req.Owner.Phone,
					Role:         models.RoleCashier,
					PasswordHash: string(ownerHash),
					IsOwner:      true,
					IsActive:     true,
				}
				return tx.Create(&defaultCashier).Error
			}

			for _, emp := range req.Employees {
				empHash, err := bcrypt.GenerateFromPassword([]byte(emp.Password), 12)
				if err != nil {
					return err
				}
				employee := models.Employee{
					TenantID:     tenant.ID,
					BranchID:     branchIDPtr,
					Name:         emp.Name,
					Phone:        emp.Phone,
					Role:         emp.Role,
					PasswordHash: string(empHash),
					IsOwner:      false,
					IsActive:     true,
				}
				if err := tx.Create(&employee).Error; err != nil {
					return err
				}
			}

			return nil
		})

		if txErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo registrar el negocio"})
			return
		}

		// Generate workspace-aware token
		resp, err := createWorkspaceTokenPair(db, user, tenant.ID, "", tenant.BusinessName, string(models.RoleOwner), jwtSecret)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al generar tokens"})
			return
		}

		c.JSON(http.StatusCreated, resp)
	}
}

// resolveBusinessTypes supports both legacy single "type" and new "types" array.
func resolveBusinessTypes(b BusinessInput) []string {
	if len(b.Types) > 0 {
		return b.Types
	}
	if b.Type != "" {
		return []string{b.Type}
	}
	return []string{}
}

// validateBusinessTypes remaps legacy values to the unified taxonomy and
// rejects anything outside the whitelist. The DB CHECK would catch the
// bad value too but we want a Spanish-language error at the app layer.
// Legacy → unified mapping mirrors migration 020's UPDATE statement so
// both the startup backfill and a fresh register land on the same values.
func validateBusinessTypes(raw []string) ([]string, error) {
	legacyMap := map[string]string{
		"muebles":    models.BusinessTypeReparacionMuebles,
		"miscelanea": models.BusinessTypeEmprendimientoGen,
		"reparacion": models.BusinessTypeReparacionMuebles,
	}

	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		if mapped, ok := legacyMap[t]; ok {
			t = mapped
		}
		if _, ok := models.ValidBusinessTypes[t]; !ok {
			return nil, fmt.Errorf("tipo de negocio no válido: %q", t)
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}
