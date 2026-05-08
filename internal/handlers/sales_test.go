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

// TestCreateSale_PersistsReceiptImageURL covers the Mandatory Image
// Receipts epic at the storage boundary: when the cashier sends
// receipt_image_url in the create-sale payload, the value must land
// on the persisted Sale row. Without this guarantee the audit trail
// breaks the moment the Supabase TTL purges the blob — the URL is
// the only evidence the cashier presented a comprobante.
func TestCreateSale_PersistsReceiptImageURL(t *testing.T) {
	db := setupIsolationDB(t)

	tenantID := "tenant-receipt"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)

	branchID := "11111111-1111-1111-1111-111111111111"
	seedBranchForIso(t, db, branchID, tenantID, "Sede Única")

	productID := "c1111111-1111-1111-1111-111111111111"
	seedProductAtBranch(t, db, productID, tenantID, branchID, "Gaseosa", 50, 2500)

	r := mountSalesHandler(db, tenantID, branchID)

	receiptURL := "https://supabase.co/storage/v1/object/public/payment_receipts/abc.jpg"
	body := map[string]any{
		"payment_method":    string(models.PaymentTransfer),
		"branch_id":         branchID,
		"receipt_image_url": receiptURL,
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

	// The handler echoes the created Sale; assert the URL roundtrips
	// through both the JSON envelope and the persisted row.
	var resp struct {
		Data models.Sale `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, receiptURL, resp.Data.ReceiptImageURL,
		"the response payload must echo the stored receipt URL")

	var stored struct {
		ReceiptImageURL string `gorm:"column:receipt_image_url"`
	}
	require.NoError(t, db.Table("sales").
		Select("receipt_image_url").
		Where("tenant_id = ?", tenantID).
		Scan(&stored).Error)
	assert.Equal(t, receiptURL, stored.ReceiptImageURL,
		"the persisted Sale row must carry the receipt URL — audit trail")
}

// TestCreateSale_AllowsEmptyReceiptForCashSale guards the "informative,
// not enforcing" contract: a cash sale legitimately omits the URL and
// the handler must NOT reject it.
func TestCreateSale_AllowsEmptyReceiptForCashSale(t *testing.T) {
	db := setupIsolationDB(t)

	tenantID := "tenant-cash"
	require.NoError(t, db.Exec(`INSERT INTO tenants (id, created_at) VALUES (?, ?)`,
		tenantID, time.Now()).Error)

	branchID := "22222222-2222-2222-2222-222222222222"
	seedBranchForIso(t, db, branchID, tenantID, "Sede Única")

	productID := "c2222222-2222-2222-2222-222222222222"
	seedProductAtBranch(t, db, productID, tenantID, branchID, "Pan", 20, 500)

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

	var stored struct {
		ReceiptImageURL string `gorm:"column:receipt_image_url"`
	}
	require.NoError(t, db.Table("sales").
		Select("receipt_image_url").
		Where("tenant_id = ?", tenantID).
		Scan(&stored).Error)
	assert.Equal(t, "", stored.ReceiptImageURL,
		"cash sales must persist with empty receipt URL — informative, never enforced")
}
