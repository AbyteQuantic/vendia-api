package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func DevOnly(env string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if env == "production" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "endpoint no disponible en producción",
			})
			return
		}
		c.Next()
	}
}
