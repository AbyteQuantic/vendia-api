// Spec: specs/029-precios-multi-tier/spec.md
package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── T-08 (F029): sales handler persists price_tier enum ────────────────────
//
// The sale handler accepts an optional `price_tier` enum value in the
// body; on create the Sale row carries it as metadata for reports and
// historical detail. Default `'retail'` preserves retrocompat for any
// caller that never updates (legacy POS or older mobile builds).
//
// Reuses the sqlite isolation harness from branch_isolation_test.go.

// TestCreateSale_WithPriceTier_Persists verifies that a sale with
// price_tier='tier_2' lands on the persisted Sale row (FR-07).
func TestCreateSale_WithPriceTier_Persists(t *testing.T) {
	db := setupIsolationDB(t)

	tenantID := "tenant-tier-sale"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)

	branchID := "11111111-1111-1111-1111-111111111101"
	seedBranchForIso(t, db, branchID, tenantID, "Sede Única")

	productID := "c1111111-1111-1111-1111-111111111101"
	seedProductAtBranch(t, db, productID, tenantID, branchID, "Cemento", 50, 28500)

	r := mountSalesHandler(db, tenantID, branchID)

	body := map[string]any{
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"price_tier":     "tier_2",
		"items": []map[string]any{
			{"product_id": productID, "quantity": 1},
		},
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/sales", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp struct {
		Data models.Sale `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "tier_2", resp.Data.PriceTier,
		"el response debe reflejar price_tier='tier_2'")

	var stored struct {
		PriceTier string `gorm:"column:price_tier"`
	}
	require.NoError(t, db.Table("sales").
		Select("price_tier").
		Where("tenant_id = ?", tenantID).
		Scan(&stored).Error)
	assert.Equal(t, "tier_2", stored.PriceTier,
		"la fila Sale persistida debe llevar price_tier='tier_2' (FR-07)")
}

// TestCreateSale_NoPriceTier_DefaultsToRetail verifies the retrocompat
// path: a payload without price_tier persists as 'retail' (FR-10,
// AC-07).
func TestCreateSale_NoPriceTier_DefaultsToRetail(t *testing.T) {
	db := setupIsolationDB(t)

	tenantID := "tenant-tier-default"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)

	branchID := "22222222-2222-2222-2222-222222222202"
	seedBranchForIso(t, db, branchID, tenantID, "Sede Única")

	productID := "c2222222-2222-2222-2222-222222222202"
	seedProductAtBranch(t, db, productID, tenantID, branchID, "Arroz", 30, 3000)

	r := mountSalesHandler(db, tenantID, branchID)

	body := map[string]any{
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"items": []map[string]any{
			{"product_id": productID, "quantity": 1},
		},
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/sales", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp struct {
		Data models.Sale `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "retail", resp.Data.PriceTier,
		"sin price_tier explícito, default 'retail' (AC-07)")
}

// TestCreateSale_InvalidPriceTier_400 verifies the enum validation
// rejects values outside {retail, tier_1, tier_2, tier_3} (FR-07, AC-07).
func TestCreateSale_InvalidPriceTier_400(t *testing.T) {
	db := setupIsolationDB(t)

	tenantID := "tenant-tier-invalid"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)

	branchID := "33333333-3333-3333-3333-333333333303"
	seedBranchForIso(t, db, branchID, tenantID, "Sede Única")

	productID := "c3333333-3333-3333-3333-333333333303"
	seedProductAtBranch(t, db, productID, tenantID, branchID, "Producto", 10, 5000)

	r := mountSalesHandler(db, tenantID, branchID)

	body := map[string]any{
		"payment_method": string(models.PaymentCash),
		"branch_id":      branchID,
		"price_tier":     "xxx",
		"items": []map[string]any{
			{"product_id": productID, "quantity": 1},
		},
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/sales", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// Nothing should have persisted.
	var count int64
	db.Table("sales").Where("tenant_id = ?", tenantID).Count(&count)
	assert.Equal(t, int64(0), count,
		"un price_tier inválido aborta la creación entera (AC-07)")
}

// TestCreateSale_AllValidTiers verifies each enum member is accepted.
// Smoke-tests the four cases the CHECK constraint allows (FR-07).
func TestCreateSale_AllValidTiers(t *testing.T) {
	cases := []string{"retail", "tier_1", "tier_2", "tier_3"}
	for i, tier := range cases {
		tier := tier
		t.Run("tier="+tier, func(t *testing.T) {
			db := setupIsolationDB(t)

			tenantID := "tenant-tier-" + tier
			require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
				tenantID, time.Now()).Error)

			branchID := "44444444-4444-4444-4444-44444444440" + string(rune('0'+i))
			seedBranchForIso(t, db, branchID, tenantID, "Sede")

			productID := "c4444444-4444-4444-4444-44444444440" + string(rune('0'+i))
			seedProductAtBranch(t, db, productID, tenantID, branchID, "Item", 10, 1000)

			r := mountSalesHandler(db, tenantID, branchID)

			body := map[string]any{
				"payment_method": string(models.PaymentCash),
				"branch_id":      branchID,
				"price_tier":     tier,
				"items": []map[string]any{
					{"product_id": productID, "quantity": 1},
				},
			}
			raw, _ := json.Marshal(body)
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/sales", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

			var stored struct {
				PriceTier string `gorm:"column:price_tier"`
			}
			require.NoError(t, db.Table("sales").
				Select("price_tier").
				Where("tenant_id = ?", tenantID).
				Scan(&stored).Error)
			assert.Equal(t, tier, stored.PriceTier)
		})
	}
}
