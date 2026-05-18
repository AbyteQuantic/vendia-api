// Spec: specs/014-inventario-solido-scope-sede/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupProductBranchDB migrates the schema CreateProduct touches plus
// Branch (needed to resolve the default sede) and a hand-crafted tenants
// table.
func setupProductBranchDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Branch{},
		&models.Product{},
		&models.InventoryMovement{},
	))
	return db
}

// seedProductBranch inserts a branch and returns its id.
func seedProductBranch(t *testing.T, db *gorm.DB, tenantID string, createdAt time.Time) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Create(&models.Branch{
		BaseModel: models.BaseModel{ID: id, CreatedAt: createdAt},
		TenantID:  tenantID,
		Name:      "Sede " + id[:4],
		IsActive:  true,
	}).Error)
	return id
}

// mountCreateProductWithBranch mounts CreateProduct with a tenant claim
// and an OPTIONAL branch claim. An empty branchID reproduces the
// mono-sede owner whose JWT carries no branch_id claim — the exact bug
// Feature 014 fixes.
func mountCreateProductWithBranch(db *gorm.DB, tenantID, branchID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		if branchID != "" {
			c.Set(middleware.BranchIDKey, branchID)
		}
		c.Next()
	})
	r.POST("/products", handlers.CreateProduct(db, nil))
	return r
}

// TestCreateProduct_NoBranchClaim_AssignsDefaultBranch verifies FR-02 /
// AC-02: a mono-sede owner whose JWT has no branch claim still gets a
// product scoped to the tenant's only sede — never branch_id NULL.
func TestCreateProduct_NoBranchClaim_AssignsDefaultBranch(t *testing.T) {
	db := setupProductBranchDB(t)
	tenantID := uuid.NewString()
	defaultBranch := seedProductBranch(t, db, tenantID, time.Now())

	// No branch claim on the JWT — the mono-sede bug scenario.
	r := mountCreateProductWithBranch(db, tenantID, "")

	productID := uuid.NewString()
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":    productID,
		"name":  "Llaveros",
		"price": 2000,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", productID).Error)
	require.NotNil(t, prod.BranchID, "el producto NUNCA se inserta con branch_id NULL")
	assert.Equal(t, defaultBranch, *prod.BranchID,
		"sin claim de sede, CreateProduct asigna la sede por defecto del tenant")
}

// TestCreateProduct_MultiSede_PicksOldestBranch verifies FR-02: when the
// JWT has no branch claim and the tenant has several sedes, the oldest
// (created_at mínimo) is the default.
func TestCreateProduct_MultiSede_PicksOldestBranch(t *testing.T) {
	db := setupProductBranchDB(t)
	tenantID := uuid.NewString()
	now := time.Now()
	oldest := seedProductBranch(t, db, tenantID, now.Add(-72*time.Hour))
	_ = seedProductBranch(t, db, tenantID, now.Add(-1*time.Hour))

	r := mountCreateProductWithBranch(db, tenantID, "")

	productID := uuid.NewString()
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":    productID,
		"name":  "Producto multi-sede",
		"price": 1500,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", productID).Error)
	require.NotNil(t, prod.BranchID)
	assert.Equal(t, oldest, *prod.BranchID,
		"la sede por defecto es la más antigua del tenant")
}

// TestCreateProduct_WithBranchClaim_KeepsClaimBranch verifies no
// regression: when the JWT DOES carry a branch claim, the product is
// scoped to that sede, not the default.
func TestCreateProduct_WithBranchClaim_KeepsClaimBranch(t *testing.T) {
	db := setupProductBranchDB(t)
	tenantID := uuid.NewString()
	now := time.Now()
	_ = seedProductBranch(t, db, tenantID, now.Add(-72*time.Hour)) // older — NOT the claim
	claimBranch := seedProductBranch(t, db, tenantID, now.Add(-1*time.Hour))

	r := mountCreateProductWithBranch(db, tenantID, claimBranch)

	productID := uuid.NewString()
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":    productID,
		"name":  "Producto sede B",
		"price": 1500,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", productID).Error)
	require.NotNil(t, prod.BranchID)
	assert.Equal(t, claimBranch, *prod.BranchID,
		"un claim de sede explícito tiene prioridad sobre la sede por defecto")
}

// TestCreateProduct_TenantWithoutBranch_KeepsNull verifies the spec
// fallback: a tenant with no sede at all keeps the current behaviour —
// the product is created with branch_id NULL (there is nothing to
// assign).
func TestCreateProduct_TenantWithoutBranch_KeepsNull(t *testing.T) {
	db := setupProductBranchDB(t)
	tenantID := uuid.NewString()
	// No branch seeded for this tenant.

	r := mountCreateProductWithBranch(db, tenantID, "")

	productID := uuid.NewString()
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":    productID,
		"name":  "Sin sede posible",
		"price": 1000,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", productID).Error)
	assert.Nil(t, prod.BranchID,
		"sin ninguna sede el comportamiento actual se mantiene (branch_id NULL)")
}

// TestCreateProduct_DuplicateID_ReturnsExisting verifies FR-05 / AC-03:
// re-POSTing a product with an `id` that already exists for the tenant
// returns 200 with the existing product — no duplicate, no raw Postgres
// products_pkey error.
func TestCreateProduct_DuplicateID_ReturnsExisting(t *testing.T) {
	db := setupProductBranchDB(t)
	tenantID := uuid.NewString()
	seedProductBranch(t, db, tenantID, time.Now())

	r := mountCreateProductWithBranch(db, tenantID, "")

	productID := uuid.NewString()
	payload := map[string]any{
		"id":    productID,
		"name":  "Gaseosa",
		"price": 2500,
		"stock": 10,
	}

	// First POST — fresh product, 201 Created.
	w1 := doJSON(t, r, http.MethodPost, "/products", payload)
	require.Equal(t, http.StatusCreated, w1.Code, w1.Body.String())

	var first struct {
		Data models.Product `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &first))
	require.Equal(t, productID, first.Data.ID)

	// Re-POST the SAME id — must be idempotent: 200 with the existing row.
	w2 := doJSON(t, r, http.MethodPost, "/products", payload)
	require.Equal(t, http.StatusOK, w2.Code,
		"un id de producto repetido debe devolver 200, no un 500 de products_pkey: "+w2.Body.String())

	var second struct {
		Data models.Product `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &second))
	assert.Equal(t, productID, second.Data.ID, "se devuelve el producto existente")
	assert.Equal(t, first.Data.Name, second.Data.Name)

	// No raw Postgres error leaked.
	assert.NotContains(t, w2.Body.String(), "duplicate key")
	assert.NotContains(t, w2.Body.String(), "products_pkey")

	// Exactly ONE product row for that id — no duplicate.
	var count int64
	require.NoError(t, db.Model(&models.Product{}).
		Where("id = ? AND tenant_id = ?", productID, tenantID).
		Count(&count).Error)
	assert.Equal(t, int64(1), count, "el producto no se duplica en la BD")

	// And the initial_stock kardex movement was logged exactly ONCE —
	// the duplicate re-POST must not log a second movement.
	var movCount int64
	require.NoError(t, db.Model(&models.InventoryMovement{}).
		Where("product_id = ? AND movement_type = ?",
			productID, models.MovementInitialStock).
		Count(&movCount).Error)
	assert.Equal(t, int64(1), movCount,
		"un re-POST duplicado no registra un segundo movimiento de kardex")
}

// TestCreateProduct_FreshID_StillCreates201 verifies no regression: a
// brand-new id is unaffected by the idempotency check.
func TestCreateProduct_FreshID_StillCreates201(t *testing.T) {
	db := setupProductBranchDB(t)
	tenantID := uuid.NewString()
	seedProductBranch(t, db, tenantID, time.Now())

	r := mountCreateProductWithBranch(db, tenantID, "")

	productID := uuid.NewString()
	w := doJSON(t, r, http.MethodPost, "/products", map[string]any{
		"id":    productID,
		"name":  "Producto nuevo",
		"price": 1200,
	})
	require.Equal(t, http.StatusCreated, w.Code,
		"un id nuevo se sigue creando con 201: "+w.Body.String())

	var resp struct {
		Data models.Product `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, productID, resp.Data.ID)
}
