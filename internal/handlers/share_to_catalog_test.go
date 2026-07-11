// Spec: specs/096-foto-referencia-verificada/spec.md (Adenda A)
package handlers_test

import (
	"context"
	"net/http"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupShareToCatalogDB hand-crafts catalog_products/catalog_images with a
// SQLite pseudo-UUID default (Postgres' gen_random_uuid() doesn't exist
// here) and AutoMigrates products (Product embeds BaseModel, which already
// generates its own client-side UUID via BeforeCreate).
func setupShareToCatalogDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}))
	// Tabla mínima de tenants para el gate de ToS (Spec 103): el handler solo
	// selecciona id + terms_accepted_version.
	require.NoError(t, db.Exec(`
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY,
			terms_accepted_version TEXT DEFAULT '',
			deleted_at DATETIME
		);
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE catalog_products (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			name TEXT NOT NULL, normalized_name TEXT,
			brand TEXT, image_url TEXT, barcode TEXT, sku TEXT,
			presentation TEXT, content TEXT, category TEXT,
			is_ai_enhanced BOOLEAN DEFAULT false, source TEXT DEFAULT 'off',
			fetched_at DATETIME, created_at DATETIME, updated_at DATETIME,
			status TEXT DEFAULT 'pending', verified_at DATETIME,
			last_checked_at DATETIME, license TEXT, source_url TEXT
		);
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE catalog_images (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			catalog_product_id TEXT NOT NULL,
			image_url TEXT NOT NULL, storage_key TEXT NOT NULL DEFAULT '',
			created_by_tenant_id TEXT NOT NULL, is_accepted BOOLEAN DEFAULT false,
			created_at DATETIME, updated_at DATETIME
		);
	`).Error)
	return db
}

// fakePhotoVerifier — doble de test del gate IA de Spec 103.
type fakePhotoVerifier struct {
	ok  bool
	err error
}

func (f fakePhotoVerifier) VerifyImageMatchesProduct(_ context.Context, _, _, _ string) (bool, error) {
	return f.ok, f.err
}

// seedTenantWithCurrentTerms deja al tenant con la versión VIGENTE de los
// ToS aceptada — requisito del gate de Spec 103 para compartir fotos.
func seedTenantWithCurrentTerms(t *testing.T, db *gorm.DB, tenantID string) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, terms_accepted_version) VALUES (?, ?)`,
		tenantID, models.CatalogTermsVersion,
	).Error)
}

func mountShareToCatalog(db *gorm.DB, tenantID string, verifier handlers.PhotoVerifier) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	catalogSvc := services.NewCatalogService(db, nil)
	r.POST("/products/:id/share-to-catalog", handlers.ShareProductPhotoToCatalog(db, catalogSvc, verifier))
	return r
}

// TestShareProductPhotoToCatalog_HappyPath_StaysPendingAlone verifies the
// consent flow works end-to-end: a tenant's own product (with barcode +
// photo) can be shared, and a single tenant sharing alone leaves the
// catalog product pending (AC-08 — needs a second distinct tenant).
func TestShareProductPhotoToCatalog_HappyPath_StaysPendingAlone(t *testing.T) {
	db := setupShareToCatalogDB(t)
	const tenant = "tenant-a"
	product := models.Product{
		TenantID: tenant, Name: "Coca-Cola 400ml", Price: 2500,
		Barcode: "7702090000012", PhotoURL: "https://r2.vendia.store/tenant-a/coca.jpg",
	}
	require.NoError(t, db.Create(&product).Error)
	seedTenantWithCurrentTerms(t, db, tenant)

	r := mountShareToCatalog(db, tenant, fakePhotoVerifier{ok: true})
	w := doJSON(t, r, http.MethodPost, "/products/"+product.ID+"/share-to-catalog", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var status string
	db.Table("catalog_products").Where("barcode = ?", "7702090000012").Pluck("status", &status)
	assert.Equal(t, "pending", status)
}

// TestShareProductPhotoToCatalog_MissingBarcode_ReturnsBadRequest.
func TestShareProductPhotoToCatalog_MissingBarcode_ReturnsBadRequest(t *testing.T) {
	db := setupShareToCatalogDB(t)
	const tenant = "tenant-a"
	product := models.Product{
		TenantID: tenant, Name: "Producto sin barcode", Price: 1000,
		PhotoURL: "https://r2.vendia.store/tenant-a/sin-barcode.jpg",
	}
	require.NoError(t, db.Create(&product).Error)

	r := mountShareToCatalog(db, tenant, fakePhotoVerifier{ok: true})
	w := doJSON(t, r, http.MethodPost, "/products/"+product.ID+"/share-to-catalog", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestShareProductPhotoToCatalog_MissingPhoto_ReturnsBadRequest.
func TestShareProductPhotoToCatalog_MissingPhoto_ReturnsBadRequest(t *testing.T) {
	db := setupShareToCatalogDB(t)
	const tenant = "tenant-a"
	product := models.Product{
		TenantID: tenant, Name: "Producto sin foto", Price: 1000,
		Barcode: "7702090000099",
	}
	require.NoError(t, db.Create(&product).Error)

	r := mountShareToCatalog(db, tenant, fakePhotoVerifier{ok: true})
	w := doJSON(t, r, http.MethodPost, "/products/"+product.ID+"/share-to-catalog", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestShareProductPhotoToCatalog_CrossTenantProduct_ReturnsNotFound verifies
// tenant isolation (Art. III): a tenant cannot share a photo for a product
// that belongs to a different tenant.
func TestShareProductPhotoToCatalog_CrossTenantProduct_ReturnsNotFound(t *testing.T) {
	db := setupShareToCatalogDB(t)
	product := models.Product{
		TenantID: "tenant-owner", Name: "Producto ajeno", Price: 1000,
		Barcode: "7702090000012", PhotoURL: "https://r2.vendia.store/tenant-owner/x.jpg",
	}
	require.NoError(t, db.Create(&product).Error)

	r := mountShareToCatalog(db, "tenant-intruso", fakePhotoVerifier{ok: true})
	w := doJSON(t, r, http.MethodPost, "/products/"+product.ID+"/share-to-catalog", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestShareProductPhotoToCatalog_SecondDistinctTenant_VerifiesForSuggestion
// verifies the full consensus path via the HTTP layer: two different
// tenants sharing a photo for the same barcode promotes the catalog
// product to 'verified' (AC-09), which is what makes it eligible for
// ReferencePhotoByBarcode to suggest to a third tenant.
func TestShareProductPhotoToCatalog_SecondDistinctTenant_VerifiesForSuggestion(t *testing.T) {
	db := setupShareToCatalogDB(t)
	productA := models.Product{
		TenantID: "tenant-a", Name: "Coca-Cola 400ml", Price: 2500,
		Barcode: "7702090000012", PhotoURL: "https://r2.vendia.store/tenant-a/coca.jpg",
	}
	productB := models.Product{
		TenantID: "tenant-b", Name: "Coca-Cola 400ml", Price: 2600,
		Barcode: "7702090000012", PhotoURL: "https://r2.vendia.store/tenant-b/coca.jpg",
	}
	require.NoError(t, db.Create(&productA).Error)
	require.NoError(t, db.Create(&productB).Error)
	seedTenantWithCurrentTerms(t, db, "tenant-a")
	seedTenantWithCurrentTerms(t, db, "tenant-b")

	rA := mountShareToCatalog(db, "tenant-a", fakePhotoVerifier{ok: true})
	wA := doJSON(t, rA, http.MethodPost, "/products/"+productA.ID+"/share-to-catalog", nil)
	require.Equal(t, http.StatusOK, wA.Code, wA.Body.String())

	rB := mountShareToCatalog(db, "tenant-b", fakePhotoVerifier{ok: true})
	wB := doJSON(t, rB, http.MethodPost, "/products/"+productB.ID+"/share-to-catalog", nil)
	require.Equal(t, http.StatusOK, wB.Code, wB.Body.String())

	var status string
	db.Table("catalog_products").Where("barcode = ?", "7702090000012").Pluck("status", &status)
	assert.Equal(t, "verified", status)
}

// ── Spec 103 — B03: la vía manual exige los mismos gates que la automática ──

// Sin ToS vigentes aceptados NO hay licencia contractual sobre la foto: el
// share se rechaza con 403 y no se escribe nada en el catálogo compartido.
func TestShareProductPhotoToCatalog_SinTerminosVigentes_Rechaza403(t *testing.T) {
	db := setupShareToCatalogDB(t)
	const tenant = "tenant-sin-tos"
	// Tenant existe pero con una versión VIEJA de los términos.
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, terms_accepted_version) VALUES (?, '2025-01-01')`, tenant,
	).Error)
	product := models.Product{
		TenantID: tenant, Name: "Coca-Cola 400ml", Price: 2500,
		Barcode: "7702090000012", PhotoURL: "https://r2.vendia.store/x/coca.jpg",
	}
	require.NoError(t, db.Create(&product).Error)

	r := mountShareToCatalog(db, tenant, fakePhotoVerifier{ok: true})
	w := doJSON(t, r, http.MethodPost, "/products/"+product.ID+"/share-to-catalog", nil)

	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "terms_required")

	var n int64
	db.Table("catalog_images").Count(&n)
	assert.Equal(t, int64(0), n, "no debe aportar nada sin ToS vigentes")
}

// Si la IA no confirma que la foto corresponde al producto (mismatch, error
// de Gemini o servicio ausente), el aporte se rechaza — fail-closed.
func TestShareProductPhotoToCatalog_IANoConfirma_Rechaza422(t *testing.T) {
	casos := map[string]handlers.PhotoVerifier{
		"mismatch":        fakePhotoVerifier{ok: false},
		"error de gemini": fakePhotoVerifier{ok: true, err: assert.AnError},
		"servicio nil":    nil,
	}
	for nombre, verifier := range casos {
		t.Run(nombre, func(t *testing.T) {
			db := setupShareToCatalogDB(t)
			const tenant = "tenant-con-tos"
			seedTenantWithCurrentTerms(t, db, tenant)
			product := models.Product{
				TenantID: tenant, Name: "Coca-Cola 400ml", Price: 2500,
				Barcode: "7702090000012", PhotoURL: "https://r2.vendia.store/x/coca.jpg",
			}
			require.NoError(t, db.Create(&product).Error)

			r := mountShareToCatalog(db, tenant, verifier)
			w := doJSON(t, r, http.MethodPost, "/products/"+product.ID+"/share-to-catalog", nil)

			require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "photo_unverified")

			var n int64
			db.Table("catalog_images").Count(&n)
			assert.Equal(t, int64(0), n, "no debe aportar sin confirmación de la IA")
		})
	}
}
