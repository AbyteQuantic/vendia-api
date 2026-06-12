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
	db.AutoMigrate(
		&models.Tenant{},
		&models.Product{},
		&models.Promotion{},
		&models.TenantPaymentMethod{},
		&models.TenantCatalogConfig{},
	)
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
			IsOpen   bool           `json:"is_open"`
			Products []any          `json:"products"`
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

// TestPublicCatalog_ExposesActivePaymentMethods locks in the contract
// introduced alongside the Digital Payments Hub: a shopper landing on
// /public/catalog/:slug must see the tenant's active wallets so they
// know how to pay without DM'ing the tendero.
//
// Also asserts the two negative cases:
//   - inactive methods stay private (is_active=false)
//   - other tenants' methods never leak in
func TestPublicCatalog_ExposesActivePaymentMethods(t *testing.T) {
	db := setupStoreTestDB()
	gin.SetMode(gin.TestMode)

	slug := "pay-shop"
	tenant := models.Tenant{
		BaseModel:      models.BaseModel{ID: "tenant-pay"},
		BusinessName:   "Pay Shop",
		Phone:          "3000000001",
		StoreSlug:      &slug,
		IsDeliveryOpen: true,
	}
	db.Create(&tenant)

	// Unrelated tenant to prove isolation. Different phone to dodge
	// the UNIQUE(tenants.phone) constraint on SQLite.
	otherSlug := "other-shop"
	other := models.Tenant{
		BaseModel:    models.BaseModel{ID: "tenant-other"},
		BusinessName: "Other Shop",
		Phone:        "3000000002",
		StoreSlug:    &otherSlug,
	}
	db.Create(&other)

	// Active method on this tenant — should be exposed.
	db.Create(&models.TenantPaymentMethod{
		BaseModel:      models.BaseModel{ID: "m-nequi"},
		TenantID:       "tenant-pay",
		Name:           "Nequi",
		Provider:       "nequi",
		AccountDetails: "3001234567",
		QRImageURL:     "https://cdn.example/qr/nequi.png",
		IsActive:       true,
	})
	// Breve method with an http URL — catalog should classify it as
	// kind="link" and expose `payment_link`.
	db.Create(&models.TenantPaymentMethod{
		BaseModel:      models.BaseModel{ID: "m-breve"},
		TenantID:       "tenant-pay",
		Name:           "Breve",
		Provider:       "breve",
		AccountDetails: "https://breve.co/pay/xyz",
		IsActive:       true,
	})
	// Inactive method — must stay private. GORM `default:true` on
	// Go zero-values means we have to create and then update,
	// otherwise IsActive sneaks back to true on INSERT.
	db.Create(&models.TenantPaymentMethod{
		BaseModel:      models.BaseModel{ID: "m-davi"},
		TenantID:       "tenant-pay",
		Name:           "Daviplata",
		Provider:       "daviplata",
		AccountDetails: "3007654321",
		IsActive:       true,
	})
	db.Model(&models.TenantPaymentMethod{}).
		Where("id = ?", "m-davi").Update("is_active", false)
	// Different-tenant method — must never leak.
	db.Create(&models.TenantPaymentMethod{
		BaseModel:      models.BaseModel{ID: "m-leak"},
		TenantID:       "tenant-other",
		Name:           "Bancolombia",
		Provider:       "bancolombia",
		AccountDetails: "1234567890",
		IsActive:       true,
	})

	r := gin.New()
	r.GET("/catalog/:slug", PublicCatalog(db))
	req, _ := http.NewRequest(http.MethodGet, "/catalog/pay-shop", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var res struct {
		Data struct {
			PaymentMethods []struct {
				ID             string `json:"id"`
				Name           string `json:"name"`
				Provider       string `json:"provider"`
				Kind           string `json:"kind"`
				AccountDetails string `json:"account_details"`
				PaymentLink    string `json:"payment_link"`
				QRImageURL     string `json:"qr_image_url"`
			} `json:"payment_methods"`
		} `json:"data"`
	}
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))

	byID := map[string]struct {
		provider, kind, link, qr string
	}{}
	for _, m := range res.Data.PaymentMethods {
		byID[m.ID] = struct {
			provider, kind, link, qr string
		}{m.Provider, m.Kind, m.PaymentLink, m.QRImageURL}
	}
	assert.Equal(t, 2, len(res.Data.PaymentMethods),
		"only this tenant's active methods (Nequi + Breve) should be exposed")
	assert.Equal(t, "nequi", byID["m-nequi"].provider)
	assert.Equal(t, "wallet", byID["m-nequi"].kind,
		"Nequi without http URL should be kind=wallet")
	assert.Equal(t, "https://cdn.example/qr/nequi.png", byID["m-nequi"].qr)
	assert.Empty(t, byID["m-nequi"].link,
		"Nequi should not leak account details as payment_link")

	assert.Equal(t, "breve", byID["m-breve"].provider)
	assert.Equal(t, "link", byID["m-breve"].kind,
		"Breve with http URL must be kind=link")
	assert.Equal(t, "https://breve.co/pay/xyz", byID["m-breve"].link,
		"payment_link must carry the URL so the SPA can open it")

	_, hasInactive := byID["m-davi"]
	assert.False(t, hasInactive, "inactive method must stay private")
	_, hasLeak := byID["m-leak"]
	assert.False(t, hasLeak, "other tenants' methods must never leak in")
}

// ── T-11 (F029): catálogo público NUNCA expone tiers ────────────────────────
//
// Spec F029 §6 AC-08 + FR-09: el storefront del cliente final muestra
// únicamente el precio retail. Aun cuando el dueño tiene la capacidad ON
// y configuró los 3 tiers en el producto, el JSON público debe llevar
// solo `price` — los `price_tier_*` quedan privados (uso interno del POS).
// Este test bloquea regresiones donde alguien suma campos al
// `CatalogProduct` struct.
func TestPublicCatalog_NeverExposesPriceTiers(t *testing.T) {
	db := setupStoreTestDB()
	gin.SetMode(gin.TestMode)

	slug := "tier-shop"
	tenant := models.Tenant{
		BaseModel:        models.BaseModel{ID: "tenant-tier"},
		BusinessName:     "Depósito con tiers",
		StoreSlug:        &slug,
		IsDeliveryOpen:   true,
		EnablePriceTiers: true,
	}
	db.Create(&tenant)

	t1, t2, t3 := 25000.0, 26500.0, 28500.0
	db.Create(&models.Product{
		BaseModel:   models.BaseModel{ID: "p-cemento"},
		TenantID:    "tenant-tier",
		Name:        "Cemento Fortecem",
		Price:       28500,
		IsAvailable: true,
		Stock:       50,
		PriceTier1:  &t1,
		PriceTier2:  &t2,
		PriceTier3:  &t3,
	})

	r := gin.New()
	r.GET("/catalog/:slug", PublicCatalog(db))
	req, _ := http.NewRequest(http.MethodGet, "/catalog/tier-shop", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Unmarshal as raw map so we can assert WHICH keys exist on the
	// product. The product struct in the response must NOT carry any
	// price_tier_* key.
	var res struct {
		Data struct {
			Products []map[string]any `json:"products"`
		} `json:"data"`
	}
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	assert.Equal(t, 1, len(res.Data.Products))

	prod := res.Data.Products[0]
	assert.Equal(t, 28500.0, prod["price"], "el retail debe seguir visible")

	for _, leak := range []string{
		"price_tier_1", "price_tier_2", "price_tier_3",
	} {
		_, exists := prod[leak]
		assert.False(t, exists,
			"el catálogo público NO debe exponer %s (FR-09, AC-08)", leak)
	}
}

// F043 — el catálogo público expone los campos del PLATO de menú para que el
// front arme la sección "Menú restaurante".
func TestPublicCatalog_ExposesMenuFields(t *testing.T) {
	db := setupStoreTestDB()
	gin.SetMode(gin.TestMode)
	slug := "resto"
	tenant := models.Tenant{
		BaseModel: models.BaseModel{ID: "t-resto"}, BusinessName: "Resto",
		StoreSlug: &slug,
	}
	db.Create(&tenant)
	dish := models.Product{
		BaseModel: models.BaseModel{ID: "dish-1"}, TenantID: "t-resto",
		Name: "Bandeja Paisa", Price: 25000, IsAvailable: true,
		Category: "Platos fuertes", Description: "Frijoles, arroz, carne, chicharrón",
		Portion: "Personal", IsMenuItem: true,
	}
	db.Create(&dish)
	retail := models.Product{
		BaseModel: models.BaseModel{ID: "ret-1"}, TenantID: "t-resto",
		Name: "Coca Cola", Price: 3000, IsAvailable: true,
	}
	db.Create(&retail)
	// F044 — un SERVICIO publicable (sin inventario).
	service := models.Product{
		BaseModel: models.BaseModel{ID: "svc-1"}, TenantID: "t-resto",
		Name: "Corte de cabello", Price: 15000, IsAvailable: true,
		Category: "Servicios", Description: "Corte clásico a tijera",
		IsService: true,
	}
	db.Create(&service)

	r := gin.New()
	r.GET("/catalog/:slug", PublicCatalog(db))
	req, _ := http.NewRequest(http.MethodGet, "/catalog/resto", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var res struct {
		Data struct {
			Products []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Portion     string `json:"portion"`
				IsMenuItem  bool   `json:"is_menu_item"`
				IsService   bool   `json:"is_service"`
			} `json:"products"`
		} `json:"data"`
	}
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	assert.Len(t, res.Data.Products, 3)

	var dishes, retails, services int
	for _, p := range res.Data.Products {
		switch {
		case p.IsMenuItem:
			dishes++
			assert.Equal(t, "Bandeja Paisa", p.Name)
			assert.Equal(t, "Frijoles, arroz, carne, chicharrón", p.Description)
			assert.Equal(t, "Personal", p.Portion)
		case p.IsService:
			services++
			assert.Equal(t, "Corte de cabello", p.Name)
			assert.Equal(t, "Corte clásico a tijera", p.Description)
		default:
			retails++
		}
	}
	assert.Equal(t, 1, dishes)
	assert.Equal(t, 1, retails)
	assert.Equal(t, 1, services)
}
