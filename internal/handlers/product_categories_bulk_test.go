// Spec: specs/078-centro-tareas-unificado/spec.md
package handlers_test

import (
	"net/http"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBulkUpdateCategories_AppliesTenantScoped(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}))
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p1"}, TenantID: "t1", Name: "Lubricante", Category: ""}).Error)
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p2"}, TenantID: "t1", Name: "Perfume", Category: ""}).Error)
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "px"}, TenantID: "OTRO", Name: "Ajeno", Category: ""}).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.POST("/products/categories/bulk", handlers.BulkUpdateCategories(db))

	w := doJSON(t, r, http.MethodPost, "/products/categories/bulk", map[string]any{
		"items": []map[string]any{
			{"id": "p1", "category": "Lubricantes"},
			{"id": "p2", "category": "Perfumes"},
			{"id": "px", "category": "Hackeo"}, // de otro tenant → no debe tocarse
		},
	})
	require.Equal(t, http.StatusOK, w.Code)

	var p1, p2, px models.Product
	db.First(&p1, "id = ?", "p1")
	db.First(&p2, "id = ?", "p2")
	db.First(&px, "id = ?", "px")
	assert.Equal(t, "Lubricantes", p1.Category)
	assert.Equal(t, "Perfumes", p2.Category)
	assert.Equal(t, "", px.Category) // tenant-scoped: intacto
}
