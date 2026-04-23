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
			Theme    map[string]any `json:"theme"`
		} `json:"data"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &res)
	assert.NoError(t, err)

	// CRITICAL: should be FALSE but products should NOT be empty
	assert.False(t, res.Data.IsOpen)
	assert.NotEmpty(t, res.Data.Products)
	assert.Equal(t, 1, len(res.Data.Products))
	assert.NotEmpty(t, res.Data.Theme["primary_color"])
}

// Regression guard for the empty-catalog bug reported by the Product
// Owner: test products with `is_available=false` OR `price=0` used to
// disappear from the public catalog because of a restrictive WHERE
// clause. The online catalog is the showroom — hiding these would
// silently break new tenants whose seed data hasn't been priced yet.
func TestPublicCatalog_IncludesUnavailableAndZeroPriceProducts(t *testing.T) {
	db := setupStoreTestDB()
	gin.SetMode(gin.TestMode)

	slug := "seed-shop"
	tenant := models.Tenant{
		BaseModel:      models.BaseModel{ID: "tenant-seed"},
		BusinessName:   "Seed Shop",
		StoreSlug:      &slug,
		IsDeliveryOpen: true,
	}
	db.Create(&tenant)

	seed := []models.Product{
		{BaseModel: models.BaseModel{ID: "p-ok"}, TenantID: "tenant-seed", Name: "Pan", Price: 1500, IsAvailable: true, Stock: 10},
		{BaseModel: models.BaseModel{ID: "p-unavail"}, TenantID: "tenant-seed", Name: "Leche (agotada)", Price: 4000, IsAvailable: false, Stock: 0},
		{BaseModel: models.BaseModel{ID: "p-zero"}, TenantID: "tenant-seed", Name: "Promo sin precio", Price: 0, IsAvailable: true, Stock: 3},
	}
	for _, p := range seed {
		db.Create(&p)
	}

	r := gin.New()
	r.GET("/catalog/:slug", PublicCatalog(db))
	req, _ := http.NewRequest(http.MethodGet, "/catalog/seed-shop", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var res struct {
		Data struct {
			Products []struct {
				UUID string `json:"uuid"`
			} `json:"products"`
		} `json:"data"`
	}
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))

	ids := map[string]bool{}
	for _, p := range res.Data.Products {
		ids[p.UUID] = true
	}
	assert.True(t, ids["p-ok"], "available priced product must render")
	assert.True(t, ids["p-unavail"], "is_available=false must still render (UI handles 'Agotado')")
	assert.True(t, ids["p-zero"], "price=0 must still render (pending price)")
}
