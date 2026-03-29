package handlers

import (
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func SyncBatch(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req services.SyncRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		svc := services.NewSyncService(db)
		resp, err := svc.ProcessBatch(tenantID, req)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al procesar sincronización"})
			return
		}

		c.JSON(http.StatusOK, resp)
	}
}
