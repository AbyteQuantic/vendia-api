// Spec: specs/001-insumos-recetas/spec.md
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupKardexDB migrates the schema ProductKardex touches: Product (the
// vendible item), Ingredient (the insumo) and InventoryMovement (the
// kardex trail, which carries movements for BOTH).
func setupKardexDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Product{},
		&models.Ingredient{},
		&models.InventoryMovement{},
		&models.Branch{},
	))
	return db
}

func mountKardexHandler(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.GET("/inventory/kardex", handlers.ProductKardex(db))
	return r
}

// mountBranchKardexHandler mounts ProductKardex with a workspace-scoped
// JWT context: both the tenant and a specific branch claim are set, so
// ResolveBranchScope returns a sede-scoped resolution (BUG-W3 coverage).
func mountBranchKardexHandler(db *gorm.DB, tenantID, branchID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Set(middleware.BranchIDKey, branchID)
		c.Next()
	})
	r.GET("/inventory/kardex", handlers.ProductKardex(db))
	return r
}

// AC-07 — a recipe_consumption movement whose product_id is the UUID of
// an INSUMO (not a product) must be visible in the kardex. The kardex
// resolves the entity name from ingredients when products has no row.
func TestProductKardex_ShowsIngredientMovements(t *testing.T) {
	db := setupKardexDB(t)
	tenantID := "tenant-kardex"

	insumoID := "c1000000-0000-4000-8000-000000000070"
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: insumoID},
		TenantID:  tenantID, Name: "Arroz", Unit: models.UnitKg,
		Stock: 2.7, UnitCost: 2900,
	}).Error)

	// A recipe_consumption movement for the insumo — exactly what
	// ExplodeRecipe writes (product_id = ingredient UUID).
	require.NoError(t, db.Create(&models.InventoryMovement{
		ID:           "9c000000-0000-4000-8000-000000000070",
		TenantID:     tenantID,
		ProductID:    insumoID,
		ProductName:  "Arroz",
		MovementType: models.MovementRecipeConsumption,
		Quantity:     -0.3,
		StockBefore:  3,
		StockAfter:   2.7,
		CreatedAt:    time.Now(),
	}).Error)

	r := mountKardexHandler(db, tenantID)
	w := doJSON(t, r, http.MethodGet, "/inventory/kardex?product_id="+insumoID, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Product struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"product"`
			Movements []models.InventoryMovement `json:"movements"`
			Total     int64                      `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, insumoID, resp.Data.Product.ID)
	assert.Equal(t, "Arroz", resp.Data.Product.Name, "insumo name resolved from ingredients")
	require.Len(t, resp.Data.Movements, 1, "the recipe_consumption movement must be visible")
	assert.Equal(t, models.MovementRecipeConsumption, resp.Data.Movements[0].MovementType)
	assert.Equal(t, int64(1), resp.Data.Total)
}

// Regression — the kardex of a normal vendible product still works:
// resolving from products takes priority and movements are returned.
func TestProductKardex_ProductMovementsStillWork(t *testing.T) {
	db := setupKardexDB(t)
	tenantID := "tenant-kardex"

	productID := "a1000000-0000-4000-8000-000000000071"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Gaseosa", Price: 2500, Stock: 47,
	}).Error)
	require.NoError(t, db.Create(&models.InventoryMovement{
		ID:           "9d000000-0000-4000-8000-000000000071",
		TenantID:     tenantID,
		ProductID:    productID,
		ProductName:  "Gaseosa",
		MovementType: models.MovementSale,
		Quantity:     -3,
		StockBefore:  50,
		StockAfter:   47,
		CreatedAt:    time.Now(),
	}).Error)

	r := mountKardexHandler(db, tenantID)
	w := doJSON(t, r, http.MethodGet, "/inventory/kardex?product_id="+productID, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Product struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Stock int    `json:"stock"`
			} `json:"product"`
			Movements []models.InventoryMovement `json:"movements"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, productID, resp.Data.Product.ID)
	assert.Equal(t, "Gaseosa", resp.Data.Product.Name)
	assert.Equal(t, 47, resp.Data.Product.Stock)
	require.Len(t, resp.Data.Movements, 1)
	assert.Equal(t, models.MovementSale, resp.Data.Movements[0].MovementType)
}

// An id that exists in neither products nor ingredients for the tenant
// is a 404 — the kardex never invents an entity.
func TestProductKardex_UnknownIDReturns404(t *testing.T) {
	db := setupKardexDB(t)
	r := mountKardexHandler(db, "tenant-kardex")
	w := doJSON(t, r, http.MethodGet,
		"/inventory/kardex?product_id=99999999-0000-4000-8000-000000000099", nil)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

// Art. III — an insumo owned by another tenant is invisible in the
// kardex: the request resolves to a 404, never a cross-tenant leak.
func TestProductKardex_IngredientTenantIsolation(t *testing.T) {
	db := setupKardexDB(t)
	foreignInsumo := "c2000000-0000-4000-8000-000000000072"
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: foreignInsumo},
		TenantID:  "tenant-other", Name: "Arroz ajeno", Unit: models.UnitKg,
	}).Error)

	r := mountKardexHandler(db, "tenant-kardex")
	w := doJSON(t, r, http.MethodGet, "/inventory/kardex?product_id="+foreignInsumo, nil)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

// kardexBranchID is a tenant-owned sede UUID used by the BUG-W3 tests.
const kardexBranchID = "b1000000-0000-4000-8000-000000000080"

// AC-01 (BUG-W3) — an insumo is tenant-scoped (no sede); its
// initial_stock movement is written with branch_id = NULL. A user with
// a workspace-scoped (branch) token must still see that movement when
// querying the insumo's kardex, and stock must equal Σ movements.
func TestProductKardex_InsumoInitialStockVisibleUnderBranchScope(t *testing.T) {
	db := setupKardexDB(t)
	tenantID := "tenant-kardex"

	// The sede the cashier's token is scoped to. ResolveBranchScope
	// runs an ownership check against branches, so the row must exist.
	require.NoError(t, db.Create(&models.Branch{
		BaseModel: models.BaseModel{ID: kardexBranchID},
		TenantID:  tenantID, Name: "Sede Norte",
	}).Error)

	insumoID := "c3000000-0000-4000-8000-000000000080"
	require.NoError(t, db.Create(&models.Ingredient{
		BaseModel: models.BaseModel{ID: insumoID},
		TenantID:  tenantID, Name: "Harina", Unit: models.UnitKg,
		Stock: 10, UnitCost: 3200,
	}).Error)

	// initial_stock movement for the insumo — branch_id is NULL because
	// insumos are tenant-scoped, not sede-scoped (F001 design).
	require.NoError(t, db.Create(&models.InventoryMovement{
		ID:           "9e000000-0000-4000-8000-000000000080",
		TenantID:     tenantID,
		BranchID:     nil,
		ProductID:    insumoID,
		ProductName:  "Harina",
		MovementType: models.MovementInitialStock,
		Quantity:     10,
		StockBefore:  0,
		StockAfter:   10,
		CreatedAt:    time.Now(),
	}).Error)

	// Cashier token scoped to Sede Norte.
	r := mountBranchKardexHandler(db, tenantID, kardexBranchID)
	w := doJSON(t, r, http.MethodGet, "/inventory/kardex?product_id="+insumoID, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Product struct {
				ID    string  `json:"id"`
				Name  string  `json:"name"`
				Stock float64 `json:"stock"`
			} `json:"product"`
			Movements []models.InventoryMovement `json:"movements"`
			Total     int64                      `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, insumoID, resp.Data.Product.ID)
	require.Len(t, resp.Data.Movements, 1,
		"tenant-wide initial_stock movement must be visible under branch scope")
	assert.Equal(t, models.MovementInitialStock, resp.Data.Movements[0].MovementType)
	assert.Equal(t, int64(1), resp.Data.Total)

	// stock = Σ movimientos
	var sum float64
	for _, m := range resp.Data.Movements {
		sum += m.Quantity
	}
	assert.Equal(t, sum, resp.Data.Product.Stock, "stock must equal sum of movements")
}

// AC-02 (BUG-W3 regression) — a user scoped to Sede A sees the
// movements of Sede A and the tenant-wide ones (branch_id IS NULL),
// but NEVER the movements of Sede B. Tenant isolation (Art. III) is
// untouched: only the sede sub-filter widens to include NULL.
func TestProductKardex_BranchScopeExcludesOtherBranchKeepsNullAndOwn(t *testing.T) {
	db := setupKardexDB(t)
	tenantID := "tenant-kardex"

	branchA := "b2000000-0000-4000-8000-000000000081"
	branchB := "b3000000-0000-4000-8000-000000000082"
	for _, b := range []struct{ id, name string }{
		{branchA, "Sede A"}, {branchB, "Sede B"},
	} {
		require.NoError(t, db.Create(&models.Branch{
			BaseModel: models.BaseModel{ID: b.id},
			TenantID:  tenantID, Name: b.name,
		}).Error)
	}

	// A vendible product that lives at both sedes — branch-scoped.
	productID := "a3000000-0000-4000-8000-000000000083"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: productID},
		TenantID:  tenantID, Name: "Gaseosa", Price: 2500, Stock: 40,
	}).Error)

	branchAPtr := branchA
	branchBPtr := branchB
	movements := []models.InventoryMovement{
		{
			ID: "9f000000-0000-4000-8000-000000000084", TenantID: tenantID,
			BranchID: &branchAPtr, ProductID: productID, ProductName: "Gaseosa",
			MovementType: models.MovementSale, Quantity: -3,
			StockBefore: 43, StockAfter: 40, CreatedAt: time.Now(),
		},
		{
			ID: "9f000000-0000-4000-8000-000000000085", TenantID: tenantID,
			BranchID: &branchBPtr, ProductID: productID, ProductName: "Gaseosa",
			MovementType: models.MovementSale, Quantity: -7,
			StockBefore: 50, StockAfter: 43, CreatedAt: time.Now(),
		},
		{
			ID: "9f000000-0000-4000-8000-000000000086", TenantID: tenantID,
			BranchID: nil, ProductID: productID, ProductName: "Gaseosa",
			MovementType: models.MovementInitialStock, Quantity: 50,
			StockBefore: 0, StockAfter: 50, CreatedAt: time.Now(),
		},
	}
	for i := range movements {
		require.NoError(t, db.Create(&movements[i]).Error)
	}

	// Cashier scoped to Sede A: must see Sede A's sale + the tenant-wide
	// initial_stock, but NOT Sede B's sale.
	r := mountBranchKardexHandler(db, tenantID, branchA)
	w := doJSON(t, r, http.MethodGet, "/inventory/kardex?product_id="+productID, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Movements []models.InventoryMovement `json:"movements"`
			Total     int64                      `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Movements, 2,
		"Sede A user sees Sede A movement + tenant-wide movement, not Sede B's")
	assert.Equal(t, int64(2), resp.Data.Total)

	seen := map[string]bool{}
	for _, m := range resp.Data.Movements {
		seen[m.ID] = true
	}
	assert.True(t, seen["9f000000-0000-4000-8000-000000000084"], "Sede A movement visible")
	assert.True(t, seen["9f000000-0000-4000-8000-000000000086"], "tenant-wide movement visible")
	assert.False(t, seen["9f000000-0000-4000-8000-000000000085"], "Sede B movement must NOT leak")
}
