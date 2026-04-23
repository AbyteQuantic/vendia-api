package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupStoreTestDB() *gorm.DB {
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	db.AutoMigrate(&models.Tenant{}, &models.Product{}, &models.Promotion{})
	return db
}

func TestPublicCatalog_AlwaysShowsProducts(t *testing.T) {
	db := setupStoreTestDB()
	gin.SetMode(gin.TestMode)

	// Create a tenant that is CLOSED for delivery
	slug := "closed-shop"
	tenant := models.Tenant{
		BaseModel:      models.BaseModel{ID: "tenant-1"},
		BusinessName:   "Closed Shop",
		StoreSlug:      &slug,
		IsDeliveryOpen: false,
	}
	db.Create(&tenant)

	// Create products
	products := []models.Product{
		{
			BaseModel:   models.BaseModel{ID: "p1"},
			TenantID:    "tenant-1",
			Name:        "Product 1",
			Price:       1000,
			IsAvailable: true,
			Stock:       10,
		},
	}
	for _, p := range products {
		db.Create(&p)
	}

	r := gin.New()
	r.GET("/catalog/:slug", PublicCatalog(db))

	req, _ := http.NewRequest(http.MethodGet, "/catalog/closed-shop", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var res struct {
		Data struct {
			IsOpen   bool `json:"is_open"`
			Products []any `json:"products"`
		} `json:"data"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &res)
	assert.NoError(t, err)

	// CRITICAL: should be FALSE but products should NOT be empty
	assert.False(t, res.Data.IsOpen)
	assert.NotEmpty(t, res.Data.Products)
	assert.Equal(t, 1, len(res.Data.Products))
}
