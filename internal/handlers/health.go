package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func Ping(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "vendia-backend"})
}

// HealthDB — GET /healthz/db. Toca la DB (SELECT 1) para mantener vivo el
// proyecto Supabase free (pausa tras ~7 días de inactividad). Spec 089. El cron
// diario lo golpea. Fail-closed: si la DB no responde, 503 (no oculta el problema).
func HealthDB(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var one int
		if err := db.Raw("SELECT 1").Scan(&one).Error; err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "db_error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "db": "up"})
	}
}
