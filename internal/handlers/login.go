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
	Phone    string `json:"phone"    binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login resolves credentials across THREE sources, in priority order:
//
//  1. users table (multi-workspace) — the canonical path. Returns the
//     workspaces array when the user belongs to >1 tenant or a JWT
//     directly for the single-workspace case.
//  2. tenants table (legacy) — pre-multi-workspace owners that never
//     migrated to a User row. Plain JWT.
//  3. employees table (RBAC fallback, 2026-04-27) — when the OWNER
//     handed a credential to a staff member but the User upsert
//     never landed (legacy data, partial writes, manual DB edits).
//     We authenticate against Employee.PasswordHash, then lazily
//     UPSERT a User row + UserWorkspace so the next login flows
//     through path #1 without the fallback.
//
// Phone normalisation: we match the literal phone the caller typed
// AND the digits-only version. A merchant who typed "+57 302 279
// 8580" when they registered, and an employee who types "3022798580"
// at login, both resolve to the same row without forcing a backfill.
func Login(db *gorm.DB, jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req LoginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		phone := strings.TrimSpace(req.Phone)
		digits := digitsOnly(phone)

		// ── Path 1: users table ─────────────────────────────────
		user, ok := lookupUserByPhone(db, phone, digits)
		if ok {
			if err := bcrypt.CompareHashAndPassword(
				[]byte(user.PasswordHash), []byte(req.Password)); err == nil {
				respondWithUser(c, db, user, jwtSecret)
				return
			}
			// User row matches the phone, but the global password
			// doesn't match. Don't 401 yet — the same person can
			// own a tenant elsewhere AND have a different password
			// assigned by another tenant's owner on their Employee
			// row. Fall through to Path 3 so we still match on
			// Employee.PasswordHash. If THAT also misses, the
			// final 401 below trips.
		}

		// ── Path 2: tenants table (legacy owner) ─────────────────
		var tenant models.Tenant
		err := db.Where("phone = ?", phone).First(&tenant).Error
		if errors.Is(err, gorm.ErrRecordNotFound) && digits != "" {
			err = db.Where(phoneNormaliseSQL+" = ?", digits).
				First(&tenant).Error
		}
		if err == nil {
			if err := bcrypt.CompareHashAndPassword(
				[]byte(tenant.PasswordHash), []byte(req.Password)); err != nil {
				// Don't fall through to employees — the phone matched
				// a tenant row, the password didn't. Returning 401 here
				// avoids leaking "the password might match an employee
				// row" via timing.
				c.JSON(http.StatusUnauthorized, gin.H{
					"error": "teléfono o contraseña incorrectos",
				})
				return
			}
			resp, err := createTokenPair(db, tenant, jwtSecret)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al generar tokens"})
				return
			}
			c.JSON(http.StatusOK, resp)
			return
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al consultar negocio"})
			return
		}

		// ── Path 3: employees table (RBAC fallback) ──────────────
		//
		// One person can have multiple Employee rows (one per
		// tenant they work for) and each row can have a DIFFERENT
		// password — the dueño of every tenant assigns their own.
		// We have to iterate every row and try the password against
		// each, returning on the first match. The matching row
		// scopes the resulting JWT to its tenant (NOT to the
		// user's other workspaces) so the credential boundary
		// stays clean: a password issued by Tienda A's owner
		// only opens Tienda A.
		emps := lookupEmployeesByPhone(db, phone, digits)
		if len(emps) > 0 {
			// Track WHY no row matched so the response can give a
			// specific hint when applicable (inactive / no-pw).
			var (
				sawInactive  bool
				sawNoPw      bool
				matchedEmp   *models.Employee
			)
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
					[]byte(e.PasswordHash), []byte(req.Password)); err == nil {
					matchedEmp = e
					break
				}
			}

			if matchedEmp != nil {
				// Auth passed. Ensure a UserWorkspace exists for
				// THIS tenant so the JWT carries the right role.
				// upsertUserFromEmployee preserves any existing
				// User.password_hash — we never overwrite the
				// global credential the user uses to log in to
				// OTHER tenants.
				lazyUser, err := upsertUserFromEmployee(
					db, *matchedEmp, []byte(req.Password))
				if err != nil {
					log.Printf("[LOGIN] employee upsert failed phone=%s: %v",
						matchedEmp.Phone, err)
					respondWithEmployeeFallback(c, db, *matchedEmp, jwtSecret)
					return
				}
				// Workspace-scoped response: emit a JWT for the
				// specific tenant whose password matched. We do
				// NOT call respondWithUser here because that
				// would surface every workspace the user belongs
				// to, including tenants whose owners issued a
				// different credential. The cashier in Tienda A
				// must NOT pull JWTs for Tienda B by typing
				// Tienda A's local password.
				respondWithEmployeeWorkspace(c, db, lazyUser, *matchedEmp, jwtSecret)
				return
			}

			// No password match across the rows. Surface the most
			// helpful error we can without leaking which row was
			// involved.
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
			// Mixed or all-with-passwords-but-wrong: canonical 401.
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "teléfono o contraseña incorrectos",
			})
			return
		}

		// ── No match anywhere ────────────────────────────────────
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "teléfono o contraseña incorrectos",
		})
	}
}

// phoneNormaliseSQL strips the most common non-digit decorations a
// human types (spaces, dashes, +, parentheses) from a column value
// so we can match a "+57 302 279 8580" stored phone against a
// "3022798580" login attempt — and vice versa. Works on both
// Postgres (production) and SQLite (tests) because REPLACE is the
// SQL-92 spelling. Anything more exotic (regexp_replace, translate)
// is engine-specific and pulls a worse portability story.
const phoneNormaliseSQL = `REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(phone, ' ', ''), '-', ''), '+', ''), '(', ''), ')', '')`

// lookupUserByPhone tries the literal phone first, then the
// digits-only normalisation. Returns the matched user + true.
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
// phone, across all tenants. Plural matters because the same
// person can be cashier at Tienda A AND owner at Tienda B —
// each tenant's owner sets a DIFFERENT password on their
// respective Employee row, and the login flow needs to try every
// hash, not just the first one ordered by the index.
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
// employee authenticates via the fallback path. Idempotent — if the
// User already exists by some other path we just attach the
// workspace (without touching the existing password hash).
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

	// Workspace upsert — keep role + branch in sync with the
	// Employee row.
	wsRole := models.RoleWSCashier
	switch {
	case emp.IsOwner:
		wsRole = models.RoleOwner
	case emp.Role == models.RoleAdmin:
		wsRole = models.RoleWSAdmin
	}

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

// respondWithUser is the shared writer for paths 1 + 3. Loads the
// user's workspaces and returns either the multi-workspace selector
// payload OR the final JWT pair.
func respondWithUser(c *gin.Context, db *gorm.DB, user models.User, jwtSecret string) {
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
		return
	}

	type WorkspaceOption struct {
		WorkspaceID string `json:"workspace_id"`
		TenantID    string `json:"tenant_id"`
		TenantName  string `json:"tenant_name"`
		BranchID    string `json:"branch_id,omitempty"`
		BranchName  string `json:"branch_name,omitempty"`
		Role        string `json:"role"`
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

	tempToken, _ := auth.GenerateToken(user.ID, user.Phone, "", jwtSecret)
	c.JSON(http.StatusOK, gin.H{
		"workspaces": options,
		"temp_token": tempToken,
		"user_id":    user.ID,
		"user_name":  user.Name,
	})
}

// respondWithEmployeeWorkspace emits a JWT scoped to the SINGLE
// tenant whose Employee row matched the password. We deliberately
// don't surface other workspaces the same person might belong to —
// a credential issued by Tienda A's owner cannot mint JWTs for
// Tienda B even though the underlying User row may link to both.
func respondWithEmployeeWorkspace(
	c *gin.Context,
	db *gorm.DB,
	user models.User,
	emp models.Employee,
	jwtSecret string,
) {
	var tenant models.Tenant
	if err := db.First(&tenant, "id = ?", emp.TenantID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "error al cargar negocio"})
		return
	}
	branchID := ""
	if emp.BranchID != nil {
		branchID = *emp.BranchID
	}
	wsRole := string(models.RoleWSCashier)
	if emp.IsOwner {
		wsRole = string(models.RoleOwner)
	} else if emp.Role == models.RoleAdmin {
		wsRole = string(models.RoleWSAdmin)
	}
	resp, err := createWorkspaceTokenPair(
		db, user, emp.TenantID, branchID, tenant.BusinessName, wsRole, jwtSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "error al generar tokens"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// respondWithEmployeeFallback is the safety net for the rare case
// where the User upsert failed mid-flight (e.g. unique-index race).
// We mint a workspace JWT directly from the Employee row so the
// cashier can keep working; the next login retries the upsert.
func respondWithEmployeeFallback(
	c *gin.Context,
	db *gorm.DB,
	emp models.Employee,
	jwtSecret string,
) {
	pseudo := models.User{
		BaseModel: models.BaseModel{ID: emp.ID},
		Phone:     emp.Phone,
		Name:      emp.Name,
	}
	respondWithEmployeeWorkspace(c, db, pseudo, emp, jwtSecret)
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
