// Spec: specs/023-capacidades-opcionales-negocio/spec.md
// Spec: specs/028-copy-fiar-credito-configurable/spec.md
package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
)

// ── T-06: business profile update recalculates feature_flags (Spec F023) ────
//
// These tests require Docker PostgreSQL and skip gracefully without it.

// setupProfileSuite registers a fresh tienda_barrio tenant via the register
// endpoint, then returns (tenantID, router-for-profile, db).
// Skips automatically if Docker PostgreSQL is unavailable.
func setupProfileSuite(t *testing.T) (tenantID string, router *gin.Engine) {
	t.Helper()
	db := setupTestDB(t) // skips if no Docker

	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	// Register a tienda_barrio tenant
	w := postJSON(setupRouter(db), defaultPayload(phone))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	tenantID = resp["tenant_id"].(string)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PATCH("/api/v1/store/profile", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	}, handlers.UpdateBusinessProfile(db))

	return tenantID, r
}

// patchProfile sends a PATCH /api/v1/store/profile request.
func patchProfile(router *gin.Engine, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPatch, "/api/v1/store/profile", bytes.NewBuffer(b))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

// TestUpdateBusinessProfile_ToggleServices_RecomputesFlags verifies that
// sending config.offers_services=true in a PATCH recomputes feature_flags
// for the tenant (AC-05, FR-07).
func TestUpdateBusinessProfile_ToggleServices_RecomputesFlags(t *testing.T) {
	tenantID, router := setupProfileSuite(t)
	db := setupTestDB(t)

	var before models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&before).Error)
	assert.False(t, before.FeatureFlags.EnableServices, "precondición: services OFF")

	w := patchProfile(router, map[string]any{
		"config": map[string]any{"offers_services": true},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var after models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&after).Error)
	assert.True(t, after.FeatureFlags.EnableServices,
		"offers_services toggle debe activar enable_services")
	assert.True(t, after.FeatureFlags.EnableCustomBilling,
		"offers_services toggle debe activar enable_custom_billing")
	assert.False(t, after.FeatureFlags.EnableKDS,
		"tienda_barrio no debe tener KDS tras el toggle de services")
	assert.False(t, after.FeatureFlags.EnableTips,
		"tienda_barrio no debe tener tips tras el toggle de services")
}

// TestUpdateBusinessProfile_ToggleSellsByWeight_RecomputesFlags verifies
// config.sells_by_weight=true enables fractional units (FR-04).
func TestUpdateBusinessProfile_ToggleSellsByWeight_RecomputesFlags(t *testing.T) {
	tenantID, router := setupProfileSuite(t)
	db := setupTestDB(t)

	w := patchProfile(router, map[string]any{
		"config": map[string]any{"sells_by_weight": true},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var after models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&after).Error)
	assert.True(t, after.FeatureFlags.EnableFractionalUnits,
		"sells_by_weight debe activar enable_fractional_units")
	assert.False(t, after.FeatureFlags.EnableTables,
		"sells_by_weight no debe activar mesas")
}

// TestUpdateBusinessProfile_ToggleTables_NoKDS verifies config.has_tables=true
// grants enable_tables WITHOUT enabling KDS or Tips (Spec D3).
func TestUpdateBusinessProfile_ToggleTables_NoKDS(t *testing.T) {
	tenantID, router := setupProfileSuite(t)
	db := setupTestDB(t)

	w := patchProfile(router, map[string]any{
		"config": map[string]any{"has_tables": true},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var after models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&after).Error)
	assert.True(t, after.FeatureFlags.EnableTables,
		"has_tables toggle debe activar enable_tables")
	assert.False(t, after.FeatureFlags.EnableKDS,
		"tienda_barrio con has_tables NO debe activar KDS (D3)")
	assert.False(t, after.FeatureFlags.EnableTips,
		"tienda_barrio con has_tables NO debe activar tips (D3)")
}

// TestUpdateBusinessProfile_NoConfig_KeepsFlags verifies that a PATCH
// without a config block does NOT change feature_flags (AC-07).
func TestUpdateBusinessProfile_NoConfig_KeepsFlags(t *testing.T) {
	tenantID, router := setupProfileSuite(t)
	db := setupTestDB(t)

	var before models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&before).Error)

	w := patchProfile(router, map[string]any{
		"business_name": "Tienda Actualizada",
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var after models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&after).Error)
	assert.Equal(t, before.FeatureFlags, after.FeatureFlags,
		"PATCH sin config no debe cambiar feature_flags (AC-07)")
}

// TestUpdateBusinessProfile_DeactivateToggle verifies AC-06: a non-type-implied
// toggle can be turned OFF again. tienda_barrio: tables ON → OFF.
func TestUpdateBusinessProfile_DeactivateToggle(t *testing.T) {
	tenantID, router := setupProfileSuite(t)
	db := setupTestDB(t)

	// First: activate tables
	w1 := patchProfile(router, map[string]any{
		"config": map[string]any{"has_tables": true},
	})
	require.Equal(t, http.StatusOK, w1.Code)

	var mid models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&mid).Error)
	require.True(t, mid.FeatureFlags.EnableTables, "mesas debe estar ON tras primer PATCH")

	// Then: deactivate tables
	w2 := patchProfile(router, map[string]any{
		"config": map[string]any{"has_tables": false},
	})
	require.Equal(t, http.StatusOK, w2.Code)

	var after models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&after).Error)
	assert.False(t, after.FeatureFlags.EnableTables,
		"has_tables=false debe desactivar enable_tables en tienda_barrio (AC-06)")
}

// TestGetBusinessProfile_ReturnsFeatureFlags verifies that GET profile includes
// feature_flags. Without it the "Editar negocio" screen cannot show the current
// toggle state and would silently drop capabilities on save (Spec F023 FR-06).
func TestGetBusinessProfile_ReturnsFeatureFlags(t *testing.T) {
	tenantID, patchRouter := setupProfileSuite(t)
	db := setupTestDB(t)

	// Activate the services toggle so feature_flags carries a real ON value.
	wp := patchProfile(patchRouter, map[string]any{
		"config": map[string]any{"offers_services": true},
	})
	require.Equal(t, http.StatusOK, wp.Code, wp.Body.String())

	gin.SetMode(gin.TestMode)
	getRouter := gin.New()
	getRouter.GET("/api/v1/store/profile", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	}, handlers.GetBusinessProfile(db))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/store/profile", nil)
	getRouter.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data, ok := resp["data"].(map[string]any)
	require.True(t, ok, "la respuesta debe tener un bloque data")

	flags, ok := data["feature_flags"].(map[string]any)
	require.True(t, ok, "GET profile debe incluir feature_flags (FR-06)")
	assert.Equal(t, true, flags["enable_services"],
		"feature_flags.enable_services debe reflejar el toggle activado")
	assert.Equal(t, true, flags["enable_custom_billing"],
		"feature_flags.enable_custom_billing debe reflejar el toggle activado")
}

// ── T-04: credit_label_mode — Spec F028 ────────────────────────────────────

// setupProfileSuiteWithGet returns (tenantID, patchRouter, getRouter, db)
// for tests that need both PATCH and GET endpoints.
func setupProfileSuiteWithGet(t *testing.T) (tenantID string, patchRouter *gin.Engine, getRouter *gin.Engine) {
	t.Helper()
	db := setupTestDB(t)

	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	w := postJSON(setupRouter(db), defaultPayload(phone))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	tenantID = resp["tenant_id"].(string)

	gin.SetMode(gin.TestMode)

	pr := gin.New()
	pr.PATCH("/api/v1/store/profile", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	}, handlers.UpdateBusinessProfile(db))

	gr := gin.New()
	gr.GET("/api/v1/store/profile", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	}, handlers.GetBusinessProfile(db))

	return tenantID, pr, gr
}

func getProfile(router *gin.Engine) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/store/profile", nil)
	router.ServeHTTP(w, req)
	return w
}

// TestGetBusinessProfile_IncludesCreditLabelMode verifies that GET profile
// always includes credit_label_mode (FR-03). Default value must be "fiar".
func TestGetBusinessProfile_IncludesCreditLabelMode(t *testing.T) {
	_, _, getRouter := setupProfileSuiteWithGet(t)

	w := getProfile(getRouter)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data, ok := resp["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "fiar", data["credit_label_mode"],
		"GET profile debe incluir credit_label_mode con default 'fiar' (FR-03, AC-01)")
}

// TestUpdateBusinessProfile_CreditLabelMode_Valid verifies that PATCH with
// credit_label_mode="credit" persists and is reflected in GET (FR-02, AC-02).
func TestUpdateBusinessProfile_CreditLabelMode_Valid(t *testing.T) {
	_, patchRouter, getRouter := setupProfileSuiteWithGet(t)

	w := patchProfile(patchRouter, map[string]any{
		"credit_label_mode": "credit",
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	wg := getProfile(getRouter)
	require.Equal(t, http.StatusOK, wg.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(wg.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, "credit", data["credit_label_mode"],
		"PATCH credit_label_mode='credit' debe persistir y reflejarse en GET (FR-02)")
}

// TestUpdateBusinessProfile_CreditLabelMode_ResetToFiar verifies that
// switching back to "fiar" from "credit" also persists correctly.
func TestUpdateBusinessProfile_CreditLabelMode_ResetToFiar(t *testing.T) {
	_, patchRouter, getRouter := setupProfileSuiteWithGet(t)

	// Set to credit
	require.Equal(t, http.StatusOK,
		patchProfile(patchRouter, map[string]any{"credit_label_mode": "credit"}).Code)

	// Switch back to fiar
	w := patchProfile(patchRouter, map[string]any{"credit_label_mode": "fiar"})
	require.Equal(t, http.StatusOK, w.Code)

	wg := getProfile(getRouter)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(wg.Body.Bytes(), &resp))
	assert.Equal(t, "fiar", resp["data"].(map[string]any)["credit_label_mode"])
}

// TestUpdateBusinessProfile_CreditLabelMode_Invalid verifies that PATCH with
// an invalid credit_label_mode returns 400 and does NOT persist (FR-02, AC-07).
func TestUpdateBusinessProfile_CreditLabelMode_Invalid(t *testing.T) {
	_, patchRouter, getRouter := setupProfileSuiteWithGet(t)

	w := patchProfile(patchRouter, map[string]any{
		"credit_label_mode": "credito_libre",
	})
	require.Equal(t, http.StatusBadRequest, w.Code,
		"un credit_label_mode inválido debe devolver 400 (AC-07)")

	var errResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.NotEmpty(t, errResp["error"], "debe incluir un mensaje de error")

	// Confirm no persistence happened
	wg := getProfile(getRouter)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(wg.Body.Bytes(), &resp))
	assert.Equal(t, "fiar", resp["data"].(map[string]any)["credit_label_mode"],
		"el modo inválido no debe haber sido persistido")
}
