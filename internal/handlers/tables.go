package handlers

import (
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ListTables(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var tables []models.Table
		if err := db.Where("tenant_id = ? AND is_active = ?", tenantID, true).
			Order("grid_y ASC, grid_x ASC").
			Find(&tables).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener mesas"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": tables, "count": len(tables)})
	}
}

func CreateTable(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ID       string `json:"id"`
		Label    string `json:"label" binding:"required"`
		GridX    int    `json:"grid_x"`
		GridY    int    `json:"grid_y"`
		Capacity int    `json:"capacity"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id must be a valid UUID v4"})
			return
		}

		capacity := req.Capacity
		if capacity <= 0 {
			capacity = 4
		}

		table := models.Table{
			TenantID: tenantID,
			Label:    req.Label,
			GridX:    req.GridX,
			GridY:    req.GridY,
			Capacity: capacity,
			IsActive: true,
		}
		if req.ID != "" {
			table.ID = req.ID
		}

		if err := db.Create(&table).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear mesa"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": table})
	}
}

func UpdateTable(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Label    *string `json:"label"`
		GridX    *int    `json:"grid_x"`
		GridY    *int    `json:"grid_y"`
		Capacity *int    `json:"capacity"`
		IsActive *bool   `json:"is_active"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		tableID := c.Param("id")

		var table models.Table
		if err := db.Where("id = ? AND tenant_id = ?", tableID, tenantID).
			First(&table).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "mesa no encontrada"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.Label != nil {
			updates["label"] = *req.Label
		}
		if req.GridX != nil {
			updates["grid_x"] = *req.GridX
		}
		if req.GridY != nil {
			updates["grid_y"] = *req.GridY
		}
		if req.Capacity != nil {
			updates["capacity"] = *req.Capacity
		}
		if req.IsActive != nil {
			updates["is_active"] = *req.IsActive
		}

		if err := db.Model(&table).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar mesa"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": table})
	}
}

// SyncTables performs a bulk upsert within a transaction.
// Tables in the array are created or updated. Tables NOT in the array
// that belong to the tenant are soft-deactivated (is_active = false).
// POST /api/v1/store/tables/sync
func SyncTables(db *gorm.DB) gin.HandlerFunc {
	type TableInput struct {
		ID       string `json:"id"`
		Label    string `json:"label" binding:"required"`
		GridX    int    `json:"grid_x"`
		GridY    int    `json:"grid_y"`
		Capacity int    `json:"capacity"`
	}

	type Request struct {
		Tables []TableInput `json:"tables" binding:"required"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		txErr := db.Transaction(func(tx *gorm.DB) error {
			// Collect IDs of tables in the payload
			activeIDs := make([]string, 0, len(req.Tables))

			for _, t := range req.Tables {
				capacity := t.Capacity
				if capacity <= 0 {
					capacity = 4
				}

				if t.ID != "" && models.IsValidUUID(t.ID) {
					// Try to update existing
					result := tx.Model(&models.Table{}).
						Where("id = ? AND tenant_id = ?", t.ID, tenantID).
						Updates(map[string]any{
							"label":     t.Label,
							"grid_x":    t.GridX,
							"grid_y":    t.GridY,
							"capacity":  capacity,
							"is_active": true,
						})
					if result.RowsAffected > 0 {
						activeIDs = append(activeIDs, t.ID)
						continue
					}
				}

				// Create new table
				newTable := models.Table{
					TenantID: tenantID,
					Label:    t.Label,
					GridX:    t.GridX,
					GridY:    t.GridY,
					Capacity: capacity,
					IsActive: true,
				}
				if t.ID != "" && models.IsValidUUID(t.ID) {
					newTable.ID = t.ID
				}
				if err := tx.Create(&newTable).Error; err != nil {
					return err
				}
				activeIDs = append(activeIDs, newTable.ID)
			}

			// Deactivate tables not in the payload
			if len(activeIDs) > 0 {
				tx.Model(&models.Table{}).
					Where("tenant_id = ? AND id NOT IN ? AND is_active = ?", tenantID, activeIDs, true).
					Update("is_active", false)
			} else {
				// No tables sent — deactivate all
				tx.Model(&models.Table{}).
					Where("tenant_id = ? AND is_active = ?", tenantID, true).
					Update("is_active", false)
			}

			return nil
		})

		if txErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al sincronizar mesas"})
			return
		}

		// Return updated list
		var tables []models.Table
		db.Where("tenant_id = ? AND is_active = ?", tenantID, true).
			Order("grid_y ASC, grid_x ASC").
			Find(&tables)

		c.JSON(http.StatusOK, gin.H{"data": tables, "count": len(tables)})
	}
}
