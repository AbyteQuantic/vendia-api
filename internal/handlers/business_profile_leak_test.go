// Spec: specs/106-onboarding-conversacional-agente/spec.md
//
// Regression for the type-implied flag leak (plan 106 §6/T-10): the register
// path suppresses type-implied capabilities (F037 minimal dashboard), but the
// PATCH used to RE-DERIVE feature_flags from business_types — so the first
// unrelated toggle silently re-activated everything the type implies (KDS,
// Tips, Tables, Services, Commissions), corrupting what Vendi configured.
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
)

// setupProfileSuiteWithType registers a tenant of the given business type and
// returns (tenantID, patch router). Mirrors setupProfileSuite.
func setupProfileSuiteWithType(t *testing.T, businessType string) (string, *gin.Engine) {
	t.Helper()
	db := setupTestDB(t)

	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	payload := defaultPayload(phone)
	payload.Business.Type = businessType
	w := postJSON(setupRouter(db), payload)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	tenantID := resp["tenant_id"].(string)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PATCH("/api/v1/store/profile", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	}, handlers.UpdateBusinessProfile(db))
	return tenantID, r
}

func TestUpdateBusinessProfile_NoTypeImpliedLeak_Restaurante(t *testing.T) {
	tenantID, router := setupProfileSuiteWithType(t, models.BusinessTypeRestaurante)
	db := setupTestDB(t)

	var before models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&before).Error)
	require.False(t, before.FeatureFlags.EnableKDS, "precondición F037: KDS OFF al registrarse")
	require.False(t, before.FeatureFlags.EnableTables, "precondición F037: mesas OFF al registrarse")
	require.False(t, before.FeatureFlags.EnableTips, "precondición F037: tips OFF al registrarse")

	// An UNRELATED toggle must not resurrect type-implied capabilities (AC-08).
	w := patchProfile(router, map[string]any{
		"config": map[string]any{"enable_quotes": true},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var after models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&after).Error)
	assert.True(t, after.EnableQuotes, "el toggle pedido sí se aplica")
	assert.False(t, after.FeatureFlags.EnableKDS, "FUGA: KDS no debe reactivarse solo")
	assert.False(t, after.FeatureFlags.EnableTables, "FUGA: mesas no debe reactivarse sola")
	assert.False(t, after.FeatureFlags.EnableTips, "FUGA: tips no debe reactivarse solo")
}

func TestUpdateBusinessProfile_NoTypeImpliedLeak_Peluqueria(t *testing.T) {
	tenantID, router := setupProfileSuiteWithType(t, models.BusinessTypePeluqueria)
	db := setupTestDB(t)

	var before models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&before).Error)
	require.False(t, before.FeatureFlags.EnableStaffCommissions,
		"precondición F037: comisiones OFF al registrarse")

	w := patchProfile(router, map[string]any{
		"config": map[string]any{"enable_customer_management": true},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var after models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&after).Error)
	assert.False(t, after.FeatureFlags.EnableStaffCommissions,
		"FUGA: comisiones no debe reactivarse sola")
	assert.False(t, after.FeatureFlags.EnableServices,
		"FUGA: servicios no debe reactivarse solo")
}

func TestUpdateBusinessProfile_ExplicitTogglesStillWork_Restaurante(t *testing.T) {
	// The fix must preserve, not freeze: explicit toggles keep working.
	tenantID, router := setupProfileSuiteWithType(t, models.BusinessTypeRestaurante)
	db := setupTestDB(t)

	w := patchProfile(router, map[string]any{
		"config": map[string]any{"has_tables": true},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var mid models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&mid).Error)
	assert.True(t, mid.FeatureFlags.EnableTables)

	// And a later unrelated PATCH preserves the explicit ON.
	w2 := patchProfile(router, map[string]any{
		"config": map[string]any{"enable_quotes": true},
	})
	require.Equal(t, http.StatusOK, w2.Code)

	var after models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&after).Error)
	assert.True(t, after.FeatureFlags.EnableTables, "mesas explícita debe preservarse")
	assert.False(t, after.FeatureFlags.EnableKDS, "KDS sigue OFF (no type-implied revival)")
}
