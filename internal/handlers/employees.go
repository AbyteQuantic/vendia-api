package handlers

import (
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func ListEmployees(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var employees []models.Employee
		if err := db.Where("tenant_id = ? AND is_active = true", tenantID).
			Order("created_at ASC").
			Find(&employees).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener empleados"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": employees})
	}
}

func CreateEmployee(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ID       string              `json:"id"`
		Name     string              `json:"name"      binding:"required"`
		Phone    string              `json:"phone"`
		Pin      string              `json:"pin"       binding:"required,len=4"`
		Role     models.EmployeeRole `json:"role"      binding:"required"`
		Password string              `json:"password"  binding:"required,min=4"`
		// BranchID is mandatory in Phase 5 — every employee must
		// belong to a sede so inventory / sales reads can filter by
		// it. See migration 025 for the DB-side backfill of legacy
		// rows; new creates have no excuse to skip it.
		BranchID string `json:"branch_id" binding:"required"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id debe ser un UUID v4 válido"})
			return
		}

		if req.Role != models.RoleAdmin && req.Role != models.RoleCashier {
			c.JSON(http.StatusBadRequest, gin.H{"error": "role debe ser 'admin' o 'cashier'"})
			return
		}

		if !models.IsValidUUID(req.BranchID) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "branch_id debe ser un UUID válido",
			})
			return
		}

		// Verify the branch belongs to this tenant. Cross-tenant
		// branch assignment would let a crafted request attach an
		// employee to another tenant's sede.
		var ownedCount int64
		db.Model(&models.Branch{}).
			Where("id = ? AND tenant_id = ?", req.BranchID, tenantID).
			Count(&ownedCount)
		if ownedCount == 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "la sucursal no pertenece al negocio",
				"error_code": "branch_not_owned",
			})
			return
		}

		passHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al procesar contraseña"})
			return
		}

		pinHash, err := bcrypt.GenerateFromPassword([]byte(req.Pin), 12)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al procesar PIN"})
			return
		}

		branchID := req.BranchID
		employee := models.Employee{
			TenantID:     tenantID,
			BranchID:     &branchID,
			Name:         req.Name,
			Phone:        req.Phone,
			Pin:          string(pinHash),
			Role:         req.Role,
			PasswordHash: string(passHash),
			IsOwner:      false,
			IsActive:     true,
		}
		if req.ID != "" {
			employee.ID = req.ID
		}

		if err := db.Create(&employee).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear empleado"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": employee})
	}
}

func UpdateEmployee(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Name  *string              `json:"name"`
		Phone *string              `json:"phone"`
		Pin   *string              `json:"pin"`
		Role  *models.EmployeeRole `json:"role"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		var employee models.Employee
		if err := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&employee).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "empleado no encontrado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.Name != nil {
			updates["name"] = *req.Name
		}
		if req.Phone != nil {
			updates["phone"] = *req.Phone
		}
		if req.Pin != nil {
			if len(*req.Pin) != 4 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "el PIN debe ser de 4 dígitos"})
				return
			}
			pinHash, err := bcrypt.GenerateFromPassword([]byte(*req.Pin), 12)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al procesar PIN"})
				return
			}
			updates["pin"] = string(pinHash)
		}
		if req.Role != nil {
			updates["role"] = *req.Role
		}

		if err := db.Model(&employee).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar empleado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": employee})
	}
}

func DeleteEmployee(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		var employee models.Employee
		if err := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&employee).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "empleado no encontrado"})
			return
		}

		if employee.IsOwner {
			c.JSON(http.StatusForbidden, gin.H{"error": "no se puede desactivar al dueño"})
			return
		}

		if err := db.Model(&employee).Update("is_active", false).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al desactivar empleado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "empleado desactivado"})
	}
}

func VerifyPin(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		EmployeeUUID string `json:"employee_uuid" binding:"required"`
		Pin          string `json:"pin"           binding:"required,len=4"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var employee models.Employee
		if err := db.Where("id = ? AND tenant_id = ? AND is_active = true", req.EmployeeUUID, tenantID).
			First(&employee).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "empleado no encontrado"})
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(employee.Pin), []byte(req.Pin)); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "PIN incorrecto"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"employee_uuid": employee.ID,
				"name":          employee.Name,
				"role":          employee.Role,
				"is_owner":      employee.IsOwner,
			},
		})
	}
}
