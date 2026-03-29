package middleware

import (
	"net/http"
	"strings"
	"vendia-backend/internal/auth"

	"github.com/gin-gonic/gin"
)

const TenantIDKey = "tenant_id"
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
		c.Next()
	}
}

func GetTenantID(c *gin.Context) string {
	v, _ := c.Get(TenantIDKey)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
