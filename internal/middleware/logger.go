package middleware

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		method := c.Request.Method

		if raw != "" {
			path = path + "?" + raw
		}

		tenantID, _ := c.Get(TenantIDKey)

		log.Printf("[HTTP] %3d | %13v | %-7s %s | tenant=%v",
			status, latency, method, path, tenantID,
		)
	}
}
