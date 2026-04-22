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

type testTenant struct {
	ID           string `gorm:"primaryKey"`
	BusinessName string
}

func (testTenant) TableName() string { return "tenants" }

func setupTestDB() *gorm.DB {
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	db.AutoMigrate(&testTenant{}, &models.CatalogAnalytics{})
	return db
}

func TestAdminGetCatalogAnalytics(t *testing.T) {
	db := setupTestDB()
	gin.SetMode(gin.TestMode)

	// Seed data
	tenant := testTenant{
		ID:           "tenant-1",
		BusinessName: "Test Shop",
	}
	db.Create(&tenant)

	analytics := models.CatalogAnalytics{
		BaseModel:       models.BaseModel{ID: "analytics-1"},
		TenantID:        "tenant-1",
		ViewsCount:      100,
		OrdersGenerated: 10,
	}
	db.Create(&analytics)

	r := gin.New()
	r.GET("/analytics", AdminGetCatalogAnalytics(db))

	req, _ := http.NewRequest(http.MethodGet, "/analytics", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var results []models.CatalogAnalyticsDTO
	err := json.Unmarshal(w.Body.Bytes(), &results)
	assert.NoError(t, err)

	assert.Equal(t, 1, len(results))
	assert.Equal(t, "Test Shop", results[0].BusinessName)
	assert.Equal(t, 10.0, results[0].ConversionRate)
}

func TestAdminGetCatalogAnalyticsZeroViews(t *testing.T) {
	db := setupTestDB()
	gin.SetMode(gin.TestMode)

	tenant := testTenant{
		ID:           "tenant-2",
		BusinessName: "Zero View Shop",
	}
	db.Create(&tenant)

	db.Create(&models.CatalogAnalytics{
		BaseModel:       models.BaseModel{ID: "analytics-2"},
		TenantID:        "tenant-2",
		ViewsCount:      0,
		OrdersGenerated: 5,
	})

	r := gin.New()
	r.GET("/analytics", AdminGetCatalogAnalytics(db))

	req, _ := http.NewRequest(http.MethodGet, "/analytics", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var results []models.CatalogAnalyticsDTO
	json.Unmarshal(w.Body.Bytes(), &results)

	assert.Equal(t, 1, len(results))
	assert.Equal(t, "Zero View Shop", results[0].BusinessName)
	assert.Equal(t, 0.0, results[0].ConversionRate)
}
