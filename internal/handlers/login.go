package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"vendia-backend/internal/auth"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type LoginRequest struct {
	Phone string `json:"phone"    binding:"required"`
	// Mismo cap que en tenant_register: la UI Flutter aplica
	// LengthLimitingTextInputFormatter(8). Mantener el contrato del
	// backend simétrico evita estados raros donde una cuenta se crea
	// con >8 (vía un cliente no-Flutter) y después no se puede loguear
	// porque la UI trunca.
	Password string `json:"password" binding:"required,min=4,max=8"`
}

// Login authenticates a phone+password pair and surfaces every
// workspace the person belongs to.
//
// Identity match (any one is sufficient):
//   - users.password_hash (the user's canonical credential)
//   - employees.password_hash (the credential a tenant owner issued
//     for THAT specific tenant)
//
// One person can hold a User row PLUS multiple Employee rows across
// tenants, each with a DIFFERENT password — Tienda A's owner sets a
// hash for their cashier, Tienda B's owner sets a different hash for
// the same cashier. Any one of those passwords proves the caller's
// identity, so any one unlocks the workspace selector.
//
// The selector is permissive (shows every workspace the person
// belongs to) but the JWT is per-workspace gated: /select-workspace
// requires the password specific to the chosen tenant. That keeps
// the credential boundary clean — Tienda A's password cannot mint a
// JWT for Tienda B.
//
// Phone normalisation: literal phone first, then digits-only via
// REPLACE chain so "+57 302 279 8580" matches "3022798580".
func Login(db *gorm.DB, jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req LoginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		phone := strings.TrimSpace(req.Phone)
		digits := digitsOnly(phone)
		password := []byte(req.Password)

		user, userFound := lookupUserByPhone(db, phone, digits)
		emps := lookupEmployeesByPhone(db, phone, digits)

		var (
			identityMatched bool
			sawInactive     bool
			sawNoPw         bool
		)

		if userFound && user.PasswordHash != "" {
			if err := bcrypt.CompareHashAndPassword(
				[]byte(user.PasswordHash), password); err == nil {
				identityMatched = true
			}
		}

		for i := range emps {
			e := &emps[i]
			if !e.IsActive {
				sawInactive = true
				continue
			}
			if e.PasswordHash == "" {
				sawNoPw = true
				continue
			}
			if err := bcrypt.CompareHashAndPassword(
				[]byte(e.PasswordHash), password); err == nil {
				identityMatched = true
				if !userFound {
					lazyUser, err := upsertUserFromEmployee(db, *e, password)
					if err != nil {
						log.Printf("[LOGIN] employee upsert failed phone=%s: %v",
							e.Phone, err)
						continue
					}
					user = lazyUser
					userFound = true
				}
			}
		}

		if identityMatched && userFound {
			reconcileWorkspacesFromEmployees(db, user, emps)
			respondAfterIdentityMatch(c, db, user, password, jwtSecret)
			return
		}

		// Legacy Tenant fallback — pre-multi-workspace owners that
		// never migrated to a User row. Plain JWT, single tenant.
		var tenant models.Tenant
		err := db.Where("phone = ?", phone).First(&tenant).Error
		if errors.Is(err, gorm.ErrRecordNotFound) && digits != "" {
			err = db.Where(phoneNormaliseSQL+" = ?", digits).
				First(&tenant).Error
		}
		if err == nil {
			if cmp := bcrypt.CompareHashAndPassword(
				[]byte(tenant.PasswordHash), password); cmp == nil {
				resp, err := createTokenPair(db, tenant, jwtSecret)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "error al generar tokens"})
					return
				}
				c.JSON(http.StatusOK, resp)
				return
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al consultar negocio"})
			return
		}

		// Pick the most useful 401/403 we can without leaking the
		// existence of a specific row.
		if len(emps) > 0 {
			if sawInactive && !sawNoPw {
				c.JSON(http.StatusForbidden, gin.H{
					"error":      "tu cuenta fue desactivada por el dueño",
					"error_code": "employee_inactive",
				})
				return
			}
			if sawNoPw && !sawInactive {
				c.JSON(http.StatusUnauthorized, gin.H{
					"error":      "el dueño aún no asignó tu contraseña",
					"error_code": "no_password_set",
				})
				return
			}
		}
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "teléfono o contraseña incorrectos",
		})
	}
}

// phoneNormaliseSQL strips spaces, dashes, +, and parentheses from a
// stored phone column so the typed value matches regardless of the
// formatting variant. SQL-92 REPLACE works on Postgres + SQLite
// (tests) without engine-specific escapes.
const phoneNormaliseSQL = `REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(phone, ' ', ''), '-', ''), '+', ''), '(', ''), ')', '')`

// lookupUserByPhone tries the literal phone first, then the
// digits-only normalisation.
func lookupUserByPhone(db *gorm.DB, phone, digits string) (models.User, bool) {
	var u models.User
	if err := db.Where("phone = ?", phone).First(&u).Error; err == nil {
		return u, true
	}
	if digits != "" {
		if err := db.Where(phoneNormaliseSQL+" = ?", digits).
			First(&u).Error; err == nil {
			return u, true
		}
	}
	return models.User{}, false
}

// lookupEmployeesByPhone returns EVERY Employee row matching the
// phone across all tenants. The same person can be cashier at Tienda
// A and owner at Tienda B with different per-row password hashes;
// the login flow needs to test the typed password against every
// hash, not just the first one.
func lookupEmployeesByPhone(db *gorm.DB, phone, digits string) []models.Employee {
	var rows []models.Employee
	if err := db.Where("phone = ?", phone).Find(&rows).Error; err == nil &&
		len(rows) > 0 {
		return rows
	}
	if digits != "" {
		_ = db.Where(phoneNormaliseSQL+" = ?", digits).Find(&rows).Error
	}
	return rows
}

// upsertUserFromEmployee creates a User + UserWorkspace pair when an
// employee authenticates without a pre-existing User row. Idempotent:
// if the User row already exists we attach the workspace without
// touching its password_hash — that's the user's canonical
// credential, never overwritten by per-tenant employee hashes.
func upsertUserFromEmployee(
	db *gorm.DB,
	emp models.Employee,
	plainPassword []byte,
) (models.User, error) {
	var user models.User
	err := db.Where("phone = ?", emp.Phone).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		hash, err := bcrypt.GenerateFromPassword(plainPassword, 12)
		if err != nil {
			return user, err
		}
		user = models.User{
			Phone:        emp.Phone,
			Name:         emp.Name,
			PasswordHash: string(hash),
		}
		if err := db.Create(&user).Error; err != nil {
			return user, err
		}
	} else if err != nil {
		return user, err
	}

	wsRole := workspaceRoleFromEmployee(emp)
	var ws models.UserWorkspace
	wsErr := db.Where("user_id = ? AND tenant_id = ?", user.ID, emp.TenantID).
		First(&ws).Error
	if errors.Is(wsErr, gorm.ErrRecordNotFound) {
		ws = models.UserWorkspace{
			UserID:   user.ID,
			TenantID: emp.TenantID,
			BranchID: emp.BranchID,
			Role:     wsRole,
		}
		if err := db.Create(&ws).Error; err != nil {
			return user, err
		}
	} else if wsErr == nil {
		db.Model(&ws).Updates(map[string]any{
			"role":      wsRole,
			"branch_id": emp.BranchID,
		})
	} else {
		return user, wsErr
	}

	return user, nil
}

// reconcileWorkspacesFromEmployees ensures a UserWorkspace row exists
// for every active Employee row tied to the user. Without this, a
// person added as cashier to Tienda B AFTER their User row was
// created would never see Tienda B in the selector — the User row
// only knows about the workspaces that existed when it was made.
// Inactive employees are skipped (we don't surface workspaces the
// owner already revoked).
func reconcileWorkspacesFromEmployees(
	db *gorm.DB,
	user models.User,
	emps []models.Employee,
) {
	for _, e := range emps {
		if !e.IsActive {
			continue
		}
		wsRole := workspaceRoleFromEmployee(e)
		var ws models.UserWorkspace
		err := db.Where("user_id = ? AND tenant_id = ?", user.ID, e.TenantID).
			First(&ws).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			_ = db.Create(&models.UserWorkspace{
				UserID:   user.ID,
				TenantID: e.TenantID,
				BranchID: e.BranchID,
				Role:     wsRole,
			}).Error
			continue
		}
		if err == nil {
			db.Model(&ws).Updates(map[string]any{
				"role":      wsRole,
				"branch_id": e.BranchID,
			})
		}
	}
}

func workspaceRoleFromEmployee(e models.Employee) models.WorkspaceRole {
	// Spec 105 F3 — mapeo centralizado (waiter/chef/courier incluidos).
	return models.WorkspaceRoleForEmployee(e)
}

// respondAfterIdentityMatch loads every workspace for the user and
// either mints a JWT (single workspace + per-workspace password
// passes) or returns the selector that triggers the second password
// prompt on the client.
func respondAfterIdentityMatch(
	c *gin.Context,
	db *gorm.DB,
	user models.User,
	password []byte,
	jwtSecret string,
) {
	var workspaces []models.UserWorkspace
	db.Preload("Tenant").Preload("Branch").
		Where("user_id = ?", user.ID).
		Find(&workspaces)

	if len(workspaces) == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no tiene negocios asociados"})
		return
	}

	if len(workspaces) == 1 {
		ws := workspaces[0]
		// Even with a single workspace, the typed password must
		// match THAT workspace's credential — not just any of the
		// user's identity passwords. Prevents a user with one
		// owner workspace and one cashier workspace from collapsing
		// into a single-JWT shortcut after losing the cashier role.
		if verifyPasswordForWorkspace(db, user, ws, password) {
			emitWorkspaceJWT(c, db, user, ws, jwtSecret)
			return
		}
		respondWithSelector(c, user, workspaces, jwtSecret)
		return
	}

	respondWithSelector(c, user, workspaces, jwtSecret)
}

// emitWorkspaceJWT mints the final access+refresh pair scoped to a
// specific tenant.
func emitWorkspaceJWT(
	c *gin.Context,
	db *gorm.DB,
	user models.User,
	ws models.UserWorkspace,
	jwtSecret string,
) {
	branchID := ""
	if ws.BranchID != nil {
		branchID = *ws.BranchID
	}
	businessName := ""
	if ws.Tenant != nil {
		businessName = ws.Tenant.BusinessName
	}
	resp, err := createWorkspaceTokenPair(
		db, user, ws.TenantID, branchID, businessName, string(ws.Role), jwtSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "error al generar tokens"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// respondWithSelector returns the workspace list + a temp_token the
// client uses on /select-workspace. `requires_workspace_password`
// signals to the Flutter app that it must prompt for a password
// before completing the selection.
func respondWithSelector(
	c *gin.Context,
	user models.User,
	workspaces []models.UserWorkspace,
	jwtSecret string,
) {
	type WorkspaceOption struct {
		WorkspaceID string `json:"workspace_id"`
		TenantID    string `json:"tenant_id"`
		TenantName  string `json:"tenant_name"`
		BranchID    string `json:"branch_id,omitempty"`
		BranchName  string `json:"branch_name,omitempty"`
		Role        string `json:"role"`
	}
	options := make([]WorkspaceOption, 0, len(workspaces))
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
	tempToken, _ := auth.GenerateToken(user.ID, user.Phone, "", jwtSecret)
	c.JSON(http.StatusOK, gin.H{
		"workspaces":                  options,
		"temp_token":                  tempToken,
		"user_id":                     user.ID,
		"user_name":                   user.Name,
		"requires_workspace_password": true,
	})
}

// verifyPasswordForWorkspace tests the typed password against the
// credential authoritative for THIS workspace.
//
// Order:
//  1. Employee row for (user.phone, ws.tenant_id) — if present and
//     active and has a password, the per-tenant credential is
//     binding. We do NOT fall back to User.password_hash here: the
//     tenant owner deliberately set a tenant-specific password and
//     the global one must not bypass it.
//  2. User.password_hash — fallback for workspaces without an
//     Employee row (legacy / owner-only setups).
//
// Returns true if the typed password matches the binding credential.
func verifyPasswordForWorkspace(
	db *gorm.DB,
	user models.User,
	ws models.UserWorkspace,
	password []byte,
) bool {
	var emp models.Employee
	err := db.Where("phone = ? AND tenant_id = ?", user.Phone, ws.TenantID).
		First(&emp).Error
	if err == nil {
		if !emp.IsActive {
			return false
		}
		if emp.PasswordHash != "" {
			return bcrypt.CompareHashAndPassword(
				[]byte(emp.PasswordHash), password) == nil
		}
	}
	if user.PasswordHash != "" {
		return bcrypt.CompareHashAndPassword(
			[]byte(user.PasswordHash), password) == nil
	}
	return false
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
