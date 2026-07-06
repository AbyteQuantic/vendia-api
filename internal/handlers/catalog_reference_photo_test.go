// Spec: specs/096-foto-referencia-verificada/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"vendia-backend/internal/handlers"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReferencePhotoByBarcode_ReturnsVerifiedMatch verifies AC-01: a
// verified catalog photo for the exact barcode is returned.
func TestReferencePhotoByBarcode_ReturnsVerifiedMatch(t *testing.T) {
	db := setupLookupBarcodeDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, brand, image_url, barcode, category, source, status)
		VALUES ('cp1', 'Coca-Cola 400ml', 'Coca-Cola', 'https://off.example/coca.jpg', '7702090000012', 'bebidas', 'off', 'verified')
	`).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/catalog/reference-photo", handlers.ReferencePhotoByBarcode(db))

	w := doJSON(t, r, http.MethodGet, "/catalog/reference-photo?barcode=7702090000012", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			CatalogProductID string `json:"catalog_product_id"`
			ImageURL         string `json:"image_url"`
			Brand            string `json:"brand"`
			Name             string `json:"name"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "cp1", resp.Data.CatalogProductID)
	assert.Equal(t, "https://off.example/coca.jpg", resp.Data.ImageURL)
	assert.Equal(t, "Coca-Cola 400ml", resp.Data.Name)
}

// TestReferencePhotoByBarcode_NoMatch_ReturnsNotFound verifies AC-04: no
// verified row for that barcode → 404, no error noise.
func TestReferencePhotoByBarcode_NoMatch_ReturnsNotFound(t *testing.T) {
	db := setupLookupBarcodeDB(t)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/catalog/reference-photo", handlers.ReferencePhotoByBarcode(db))

	w := doJSON(t, r, http.MethodGet, "/catalog/reference-photo?barcode=0000000000000", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestReferencePhotoByBarcode_PendingStatus_ReturnsNotFound verifies a row
// that exists but isn't yet verified (status='pending' or 'stale') is
// never suggested — only status='verified' counts.
func TestReferencePhotoByBarcode_PendingStatus_ReturnsNotFound(t *testing.T) {
	db := setupLookupBarcodeDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, barcode, source, status)
		VALUES ('cp2', 'Producto sin verificar', '1111111111111', 'off', 'pending')
	`).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/catalog/reference-photo", handlers.ReferencePhotoByBarcode(db))

	w := doJSON(t, r, http.MethodGet, "/catalog/reference-photo?barcode=1111111111111", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestReferencePhotoByBarcode_MissingParam_ReturnsBadRequest.
func TestReferencePhotoByBarcode_MissingParam_ReturnsBadRequest(t *testing.T) {
	db := setupLookupBarcodeDB(t)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/catalog/reference-photo", handlers.ReferencePhotoByBarcode(db))

	w := doJSON(t, r, http.MethodGet, "/catalog/reference-photo", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
