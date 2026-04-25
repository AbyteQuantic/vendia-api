package handlers

import (
	"errors"
	"net/http"
	"strings"
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

// SetEmployeePassword — POST /api/v1/store/employees/:uuid/password
//
// Owner-only flow that lets the tendero hand a credential to a new
// staff member so they can log in with phone + password. The
// endpoint also resolves the multi-tenant edge case the P.O.
// flagged: a phone that is CASHIER at Tienda A, then becomes OWNER
// of Tienda B, and vice-versa.
//
// Strategy:
//
//  1. Hash and persist on Employee.PasswordHash (per-tenant audit
//     trail of the credential the owner handed out).
//  2. UPSERT into the global User table by phone:
//       - If no User row for that phone exists, create one with the
//         same hash. The employee can immediately log in.
//       - If a User row ALREADY exists, do NOT touch User.PasswordHash.
//         That row belongs to the person — the owner of THIS tenant
//         must not be able to overwrite the global credential a
//         person uses to log in to OTHER tenants. Surface a 200 with
//         password_already_set=true so the UI can show "Esta persona
//         ya tiene clave personal" instead of pretending the new pwd
//         landed.
//  3. UPSERT into UserWorkspace (user_id, tenant_id, branch_id, role)
//     so the next login by phone returns this tenant in the
//     workspaces array. Idempotent — re-running the endpoint with
//     the same role/branch is a no-op.
//
// Authorization: this is mounted on the protected /store/* group,
// which requires a JWT. We additionally check the JWT's role claim
// is owner/admin so a CASHIER can't reset a peer's password.
func SetEmployeePassword(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Password string `json:"password" binding:"required,min=6"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		role := strings.ToLower(middleware.GetRole(c))
		// Legacy tokens predate the role claim — treat them as owner
		// so single-tenant accounts that signed up before multi-
		// workspace stay working. Once they re-login through the
		// multi-tenant flow they get a proper role claim.
		isPrivileged := role == "" || role == "owner" || role == "admin"
		if !isPrivileged {
			c.JSON(http.StatusForbidden, gin.H{
				"error":      "solo el dueño puede asignar contraseñas",
				"error_code": "owner_required",
			})
			return
		}

		employeeID := c.Param("uuid")
		if !models.IsValidUUID(employeeID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "uuid inválido"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var employee models.Employee
		if err := db.Where("id = ? AND tenant_id = ?", employeeID, tenantID).
			First(&employee).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "empleado no encontrado"})
			return
		}
		if strings.TrimSpace(employee.Phone) == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "primero asigna un celular al empleado",
				"error_code": "missing_phone",
			})
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "error al procesar contraseña",
			})
			return
		}

		// 1. Per-tenant credential — overwritten freely, this is the
		//    owner's audit trail of what they gave the employee.
		if err := db.Model(&employee).
			Update("password_hash", string(hash)).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al guardar contraseña local",
				"detail": err.Error(),
			})
			return
		}

		// 2 + 3. Global User + UserWorkspace upsert.
		passwordAlreadySet := false
		var user models.User
		err = db.Where("phone = ?", employee.Phone).First(&user).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			user = models.User{
				Phone:        employee.Phone,
				Name:         employee.Name,
				PasswordHash: string(hash),
			}
			if err := db.Create(&user).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error":  "error al crear cuenta de usuario",
					"detail": err.Error(),
				})
				return
			}
		case err != nil:
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al consultar cuenta de usuario",
				"detail": err.Error(),
			})
			return
		default:
			// User already exists — DO NOT touch their password. That
			// row belongs to the person; the owner of THIS tenant
			// can't overwrite a credential used elsewhere.
			passwordAlreadySet = true
		}

		// Map employee role onto the workspace vocab. EmployeeRole is
		// admin/cashier; UserWorkspace also has owner/waiter/manager.
		// IsOwner=true on the Employee row maps to RoleOwner.
		wsRole := models.RoleWSCashier
		switch {
		case employee.IsOwner:
			wsRole = models.RoleOwner
		case employee.Role == models.RoleAdmin:
			wsRole = models.RoleWSAdmin
		}

		var ws models.UserWorkspace
		err = db.Where("user_id = ? AND tenant_id = ?", user.ID, tenantID).
			First(&ws).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			ws = models.UserWorkspace{
				UserID:   user.ID,
				TenantID: tenantID,
				BranchID: employee.BranchID,
				Role:     wsRole,
			}
			if err := db.Create(&ws).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error":  "error al vincular cuenta al negocio",
					"detail": err.Error(),
				})
				return
			}
		} else if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al consultar vínculo al negocio",
				"detail": err.Error(),
			})
			return
		} else {
			// Existing workspace — keep role + branch in sync with
			// the latest Employee row so a role change on the POS
			// surfaces in subsequent logins.
			updates := map[string]any{
				"role":      wsRole,
				"branch_id": employee.BranchID,
			}
			db.Model(&ws).Updates(updates)
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"employee_id":          employee.ID,
				"phone":                employee.Phone,
				"workspace_role":       wsRole,
				"password_already_set": passwordAlreadySet,
			},
		})
	}
}
