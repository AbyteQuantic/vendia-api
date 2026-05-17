// Spec: specs/004-idempotencia-venta-login/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Feature 004 / BUG-2 — re-POSTing a sale with an `id` already used must
// be idempotent: HTTP 200 with the EXISTING sale, no duplicate row, no
// raw Postgres `duplicate key` error leaking in English (Art. V), and —
// for product-receta sales — no second explosion of the recipe insumos
// (Art. II offline-first re-sync safety). The reused SQLite harness
// (setupSaleRecipeDB / mountSaleRecipeHandler / seedRecipeProductForSale)
// already declares `sales.id` as PRIMARY KEY, so the duplicate-insert
// failure these tests guard against is the real one production hits.

// AC-01 — a clean re-POST of a direct-product sale with the SAME `id`
// returns 200 with the original sale and creates NO duplicate row.
func TestCreateSale_DuplicateID_ReturnsExistingSale(t *testing.T) {
	db := setupSaleRecipeDB(t)
	tenantID := "tenant-idem-ac01"
	branchID := "e2222222-0000-4000-8000-000000000010"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, created_at, updated_at, tenant_id, name, is_active)
		VALUES (?, ?, ?, ?, 'Sede', 1)`, branchID, time.Now(), time.Now(), tenantID).Error)

	directID := "a2222222-0000-4000-8000-000000000010"
	require.NoError(t, db.Exec(`
		INSERT INTO products (id, created_at, updated_at, tenant_id, branch_id,
			name, price, stock, is_available, is_recipe)
		VALUES (?, ?, ?, ?, ?, 'Gaseosa', 2500, 50, 1, 0)`,
		directID, time.Now(), time.Now(), tenantID, branchID).Error)

	r := mountSaleRecipeHandler(db, tenantID, branchID)

	saleID := "9b2e0000-0000-4000-8000-000000000010"
	payload := map[string]any{
		"id":             saleID,
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"items": []map[string]any{
			{"product_id": directID, "quantity": 2},
		},
	}

	// First POST — a brand-new sale, must be created (201).
	w1 := doJSON(t, r, http.MethodPost, "/api/v1/sales", payload)
	require.Equal(t, http.StatusCreated, w1.Code, w1.Body.String())

	var first struct {
		Data models.Sale `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &first))
	require.Equal(t, saleID, first.Data.ID)

	// Re-POST the SAME id — must be idempotent: 200 with the existing sale.
	w2 := doJSON(t, r, http.MethodPost, "/api/v1/sales", payload)
	require.Equal(t, http.StatusOK, w2.Code,
		"a duplicate sale id must return 200, not a raw Postgres 400: "+w2.Body.String())

	var second struct {
		Data models.Sale `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &second))
	assert.Equal(t, saleID, second.Data.ID, "the returned sale must be the existing one")
	assert.Equal(t, first.Data.Total, second.Data.Total,
		"the existing sale's total must be echoed back unchanged")

	// No raw Postgres error leaked.
	assert.NotContains(t, w2.Body.String(), "duplicate key",
		"the response must never leak the raw Postgres error")

	// Exactly ONE sales row for that id — no duplicate.
	var saleCount int64
	require.NoError(t, db.Model(&models.Sale{}).
		Where("id = ? AND tenant_id = ?", saleID, tenantID).
		Count(&saleCount).Error)
	assert.Equal(t, int64(1), saleCount, "the sale must not be duplicated in the DB")

	// Direct-product stock decremented only ONCE (50 - 2 = 48).
	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", directID).Error)
	assert.Equal(t, 48, prod.Stock,
		"a duplicate re-POST must not decrement stock a second time")
}

// AC-02 — re-POSTing a product-receta sale with a duplicate `id` must
// NOT discount the insumos a second time. Because the duplicate is
// detected BEFORE the transaction opens, recipe explosion never runs on
// the duplicate path.
func TestCreateSale_DuplicateID_RecipeNotExplodedTwice(t *testing.T) {
	db := setupSaleRecipeDB(t)
	tenantID := "tenant-idem-ac02"
	branchID := "e3333333-0000-4000-8000-000000000010"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, created_at, updated_at, tenant_id, name, is_active)
		VALUES (?, ?, ?, ?, 'Sede', 1)`, branchID, time.Now(), time.Now(), tenantID).Error)

	f := seedRecipeProductForSale(t, db, tenantID, branchID)
	r := mountSaleRecipeHandler(db, tenantID, branchID)

	saleID := "9c3e0000-0000-4000-8000-000000000010"
	payload := map[string]any{
		"id":             saleID,
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"items": []map[string]any{
			{"product_id": f.productID, "quantity": 1},
		},
	}

	// First sale — succeeds and discounts the insumos once.
	w1 := doJSON(t, r, http.MethodPost, "/api/v1/sales", payload)
	require.Equal(t, http.StatusCreated, w1.Code, w1.Body.String())

	// Re-POST the same id — idempotent 200, NO second explosion.
	w2 := doJSON(t, r, http.MethodPost, "/api/v1/sales", payload)
	require.Equal(t, http.StatusOK, w2.Code, w2.Body.String())

	// Insumos discounted exactly once (arroz: 3 - 1*0.15 = 2.85).
	var arroz, pollo models.Ingredient
	require.NoError(t, db.First(&arroz, "id = ?", f.arrozID).Error)
	require.NoError(t, db.First(&pollo, "id = ?", f.polloID).Error)
	assert.InDelta(t, 2.85, arroz.Stock, 1e-9,
		"arroz must drop only once — no double recipe explosion")
	assert.InDelta(t, 1.80, pollo.Stock, 1e-9,
		"pollo must drop only once — no double recipe explosion")

	// Exactly two recipe_consumption movements (from the first sale only).
	var movCount int64
	require.NoError(t, db.Model(&models.InventoryMovement{}).
		Where("movement_type = ?", models.MovementRecipeConsumption).
		Count(&movCount).Error)
	assert.Equal(t, int64(2), movCount,
		"the duplicate re-POST must produce zero extra recipe_consumption movements")

	// Still exactly one sales row.
	var saleCount int64
	require.NoError(t, db.Model(&models.Sale{}).
		Where("id = ? AND tenant_id = ?", saleID, tenantID).
		Count(&saleCount).Error)
	assert.Equal(t, int64(1), saleCount, "the recipe sale must not be duplicated")
}

// AC-03 (no regression) — a brand-new sale with an `id` that has never
// been used must behave exactly as before: 201 Created, sale persisted,
// stock decremented. The idempotency check must not interfere.
func TestCreateSale_FreshID_StillCreates201(t *testing.T) {
	db := setupSaleRecipeDB(t)
	tenantID := "tenant-idem-ac03"
	branchID := "e4444444-0000-4000-8000-000000000010"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, created_at, updated_at, tenant_id, name, is_active)
		VALUES (?, ?, ?, ?, 'Sede', 1)`, branchID, time.Now(), time.Now(), tenantID).Error)

	directID := "a4444444-0000-4000-8000-000000000010"
	require.NoError(t, db.Exec(`
		INSERT INTO products (id, created_at, updated_at, tenant_id, branch_id,
			name, price, stock, is_available, is_recipe)
		VALUES (?, ?, ?, ?, ?, 'Pan', 500, 30, 1, 0)`,
		directID, time.Now(), time.Now(), tenantID, branchID).Error)

	r := mountSaleRecipeHandler(db, tenantID, branchID)

	freshID := "9d4e0000-0000-4000-8000-000000000010"
	payload := map[string]any{
		"id":             freshID,
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"items": []map[string]any{
			{"product_id": directID, "quantity": 4},
		},
	}

	w := doJSON(t, r, http.MethodPost, "/api/v1/sales", payload)
	require.Equal(t, http.StatusCreated, w.Code,
		"a fresh sale id must still be created with 201: "+w.Body.String())

	var resp struct {
		Data models.Sale `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, freshID, resp.Data.ID)
	assert.Equal(t, float64(2000), resp.Data.Total, "4 * 500 = 2000")

	var prod models.Product
	require.NoError(t, db.First(&prod, "id = ?", directID).Error)
	assert.Equal(t, 26, prod.Stock, "fresh sale must decrement stock as before (30 - 4)")
}

// AC-03 (no regression) — a sale without any `id` field still creates
// normally (server-side UUID) and is unaffected by the idempotency
// check, which only triggers on an explicitly provided id.
func TestCreateSale_NoID_StillCreates201(t *testing.T) {
	db := setupSaleRecipeDB(t)
	tenantID := "tenant-idem-noid"
	branchID := "e5555555-0000-4000-8000-000000000010"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, created_at, updated_at, tenant_id, name, is_active)
		VALUES (?, ?, ?, ?, 'Sede', 1)`, branchID, time.Now(), time.Now(), tenantID).Error)

	directID := "a5555555-0000-4000-8000-000000000010"
	require.NoError(t, db.Exec(`
		INSERT INTO products (id, created_at, updated_at, tenant_id, branch_id,
			name, price, stock, is_available, is_recipe)
		VALUES (?, ?, ?, ?, ?, 'Leche', 3000, 10, 1, 0)`,
		directID, time.Now(), time.Now(), tenantID, branchID).Error)

	r := mountSaleRecipeHandler(db, tenantID, branchID)

	payload := map[string]any{
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"items": []map[string]any{
			{"product_id": directID, "quantity": 1},
		},
	}

	w := doJSON(t, r, http.MethodPost, "/api/v1/sales", payload)
	require.Equal(t, http.StatusCreated, w.Code,
		"a sale with no id must still be created with 201: "+w.Body.String())

	var resp struct {
		Data models.Sale `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, models.IsValidUUID(resp.Data.ID),
		"a sale with no client id must receive a server-generated UUID")
}
