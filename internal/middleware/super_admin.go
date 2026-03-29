package middleware

import (
	"net/http"

	"vendia-backend/internal/auth"

	"github.com/gin-gonic/gin"
)

func SuperAdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		v, exists := c.Get(ClaimsKey)
		if !exists {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "acceso denegado"})
			return
		}

		claims, ok := v.(*auth.Claims)
		if !ok || !claims.IsSuperAdmin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "se requiere rol super_admin"})
			return
		}

		c.Next()
	}
}
