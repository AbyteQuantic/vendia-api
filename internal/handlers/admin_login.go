package handlers

import (
	"net/http"
	"strings"

	"vendia-backend/internal/auth"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type adminLoginRequest struct {
	Email    string `json:"email"    binding:"required"`
	Password string `json:"password" binding:"required"`
}

// AdminLogin authenticates against the admin_users table (separate
// from tenant login) and returns a JWT with IsSuperAdmin=true so the
// request can clear the SuperAdminOnly() middleware gate.
//
// The endpoint is mounted PUBLIC — behind the login rate limiter but
// NOT behind Auth or SuperAdminOnly (you need credentials before you
// can hold a token). Emails are lowercased + trimmed before the
// lookup; the bcrypt compare runs even on unknown emails so the
// response timing doesn't leak whether an address is in the table.
func AdminLogin(db *gorm.DB, jwtSecret string) gin.HandlerFunc {
	// Pre-computed bcrypt hash of a random string; used as the
	// decoy for unknown-email compares so the total response time
	// matches a real compare. Regenerated per process start so a
	// leaked value can't be used against the real hashes.
	decoyHash, _ := bcrypt.GenerateFromPassword([]byte("decoy-timing"), bcrypt.DefaultCost)

	return func(c *gin.Context) {
		var req adminLoginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "email y contraseña son obligatorios",
			})
			return
		}

		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "email requerido"})
			return
		}

		var admin models.AdminUser
		err := db.Where("email = ? AND is_super_admin = ?", email, true).
			First(&admin).Error

		// Run the compare either way so timing is indistinguishable
		// between "unknown email" and "wrong password".
		hash := decoyHash
		if err == nil {
			hash = []byte(admin.PasswordHash)
		}
		cmpErr := bcrypt.CompareHashAndPassword(hash, []byte(req.Password))

		if err != nil || cmpErr != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "email o contraseña incorrectos",
			})
			return
		}

		token, signErr := auth.GenerateAdminToken(
			admin.ID, admin.Email, admin.Name, jwtSecret,
		)
		if signErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo generar el token",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"token":          token,
			"tenant_id":      admin.ID,
			"owner_name":     admin.Name,
			"business_name":  "Super Admin",
			"role":           "super_admin",
			"is_super_admin": true,
		})
	}
}
