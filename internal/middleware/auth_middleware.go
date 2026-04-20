package middleware

import (
	"net/http"
	"strings"
	"vendia-backend/internal/auth"

	"github.com/gin-gonic/gin"
)

const TenantIDKey = "tenant_id"
const UserIDKey = "user_id"
const BranchIDKey = "branch_id"
const RoleKey = "role"
const ClaimsKey = "claims"

func Auth(jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "token requerido",
			})
			return
		}

		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "formato inválido: usa 'Bearer <token>'",
			})
			return
		}

		claims, err := auth.ValidateToken(parts[1], jwtSecret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "token inválido o expirado",
			})
			return
		}

		c.Set(TenantIDKey, claims.TenantID)
		c.Set(ClaimsKey, claims)
		// Multi-workspace fields (may be empty for legacy tokens)
		if claims.UserID != "" {
			c.Set(UserIDKey, claims.UserID)
		}
		if claims.BranchID != "" {
			c.Set(BranchIDKey, claims.BranchID)
		}
		if claims.Role != "" {
			c.Set(RoleKey, claims.Role)
		}
		c.Next()
	}
}

// GetTenantID returns tenant_id from JWT. Works with both old and new tokens.
func GetTenantID(c *gin.Context) string {
	v, _ := c.Get(TenantIDKey)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func GetUserID(c *gin.Context) string {
	v, _ := c.Get(UserIDKey)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func GetBranchID(c *gin.Context) string {
	v, _ := c.Get(BranchIDKey)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func GetRole(c *gin.Context) string {
	v, _ := c.Get(RoleKey)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// UUIDPtr wraps a (possibly empty) UUID string so GORM emits SQL NULL
// instead of an empty-string literal when inserting into UUID columns.
// Use this when populating *string fields from JWT claims that may be
// absent on legacy tokens.
func UUIDPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// GetUserIDPtr and GetBranchIDPtr are convenience wrappers for the two
// most common cases — read the claim and convert empty to nil in one go.
func GetUserIDPtr(c *gin.Context) *string  { return UUIDPtr(GetUserID(c)) }
func GetBranchIDPtr(c *gin.Context) *string { return UUIDPtr(GetBranchID(c)) }
