// Spec: specs/097-completar-fotos-inventario/spec.md
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

type batchSuggestion struct {
	CatalogProductID string `json:"catalog_product_id"`
	ImageURL         string `json:"image_url"`
	Brand            string `json:"brand"`
	Name             string `json:"name"`
	Verified         bool   `json:"verified"`
}

func mountBatch(t *testing.T) *gin.Engine {
	t.Helper()
	db := setupLookupBarcodeDB(t)
	// Verificada (con imagen).
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, brand, image_url, barcode, source, status)
		VALUES ('cv', 'Coca-Cola 400ml', 'Coca-Cola', 'https://r2/coca.jpg', '7702090000012', 'user', 'verified')
	`).Error)
	// Respaldo pending (con imagen) — se sugiere marcada como no verificada.
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, image_url, barcode, source, status)
		VALUES ('cp', 'Salsa Brava', 'https://off/salsa.jpg', '7702097154521', 'off', 'pending')
	`).Error)
	// Fila SIN imagen — nunca se sugiere.
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, image_url, barcode, source, status)
		VALUES ('cn', 'Sin Foto', '', '9999999999999', 'off', 'pending')
	`).Error)
	// Mismo barcode con verified + pending → gana verified.
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, image_url, barcode, source, status)
		VALUES ('cd_pend', 'Dup Pending', 'https://off/dup.jpg', '1111111111111', 'off', 'pending')
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO catalog_products (id, name, image_url, barcode, source, status)
		VALUES ('cd_ver', 'Dup Verified', 'https://r2/dup.jpg', '1111111111111', 'user', 'verified')
	`).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/catalog/reference-photos", handlers.ReferencePhotosByBarcodes(db))
	return r
}

func TestReferencePhotosByBarcodes_VerifiedAndPendingMarked(t *testing.T) {
	r := mountBatch(t)
	body := map[string]any{"barcodes": []string{
		"7702090000012", "7702097154521", "9999999999999", "0000000000000",
	}}
	w := doJSON(t, r, http.MethodPost, "/catalog/reference-photos", body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data map[string]batchSuggestion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Verificada.
	cv, ok := resp.Data["7702090000012"]
	require.True(t, ok)
	assert.True(t, cv.Verified)
	assert.Equal(t, "https://r2/coca.jpg", cv.ImageURL)

	// Respaldo pending → sugerida pero sin confirmar.
	cp, ok := resp.Data["7702097154521"]
	require.True(t, ok)
	assert.False(t, cp.Verified)
	assert.Equal(t, "https://off/salsa.jpg", cp.ImageURL)

	// Sin imagen → NO se sugiere.
	_, ok = resp.Data["9999999999999"]
	assert.False(t, ok)

	// Barcode inexistente → ausente.
	_, ok = resp.Data["0000000000000"]
	assert.False(t, ok)
}

func TestReferencePhotosByBarcodes_VerifiedWinsOverPending(t *testing.T) {
	r := mountBatch(t)
	body := map[string]any{"barcodes": []string{"1111111111111"}}
	w := doJSON(t, r, http.MethodPost, "/catalog/reference-photos", body)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data map[string]batchSuggestion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	dup, ok := resp.Data["1111111111111"]
	require.True(t, ok)
	assert.True(t, dup.Verified)
	assert.Equal(t, "https://r2/dup.jpg", dup.ImageURL)
}

func TestReferencePhotosByBarcodes_EmptyBody(t *testing.T) {
	r := mountBatch(t)
	w := doJSON(t, r, http.MethodPost, "/catalog/reference-photos", map[string]any{"barcodes": []string{}})
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data map[string]batchSuggestion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Data)
}
