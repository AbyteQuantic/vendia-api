package handlers

import (
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
	Name        string              `json:"name"         binding:"required"`
	Type        models.BusinessType `json:"type"         binding:"required"`
	RazonSocial string              `json:"razon_social"`
	NIT         string              `json:"nit"`
	Address     string              `json:"address"`
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

		var existing models.Tenant
		if err := db.Where("phone = ?", req.Owner.Phone).First(&existing).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "ese número ya está registrado"})
			return
		}

		ownerHash, err := bcrypt.GenerateFromPassword([]byte(req.Owner.Password), 12)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al procesar contraseña"})
			return
		}

		var tenant models.Tenant

		txErr := db.Transaction(func(tx *gorm.DB) error {
			tenant = models.Tenant{
				OwnerName:    req.Owner.Name,
				Phone:        req.Owner.Phone,
				PasswordHash: string(ownerHash),
				BusinessName: req.Business.Name,
				BusinessType: req.Business.Type,
				RazonSocial:  req.Business.RazonSocial,
				NIT:          req.Business.NIT,
				Address:      req.Business.Address,
				SaleTypes:    req.Config.SaleTypes,
				HasShowcases: req.Config.HasShowcases,
				HasTables:    req.Config.HasTables,
			}
			if err := tx.Create(&tenant).Error; err != nil {
				return err
			}

			if len(req.Employees) == 0 {
				defaultCashier := models.Employee{
					TenantID:     tenant.ID,
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

		resp, err := createTokenPair(db, tenant, jwtSecret)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al generar tokens"})
			return
		}

		c.JSON(http.StatusCreated, resp)
	}
}
