// Spec: specs/106-onboarding-conversacional-agente/spec.md
//
// Registro mínimo (T-12): el flujo corto de Vendi solo manda credenciales +
// términos + aviso de datos; el nombre del negocio y la configuración los
// define la conversación DESPUÉS. El payload completo de apps viejas debe
// seguir funcionando igual (Art. X).
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

	"vendia-backend/internal/models"
)

func TestTenantRegisterMinimalPayload(t *testing.T) {
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	body := map[string]any{
		"owner":                map[string]any{"name": "Bryan", "phone": phone, "password": "1234"},
		"accept_terms":         true,
		"data_notice_accepted": true,
	}
	raw, _ := json.Marshal(body)
	w := postRaw(setupRouter(db), raw)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var tenant models.Tenant
	require.NoError(t, db.Where("phone = ?", phone).First(&tenant).Error)
	assert.Equal(t, "Mi negocio", tenant.BusinessName, "placeholder hasta que Vendi pregunte")
	assert.Empty(t, tenant.BusinessTypes, "sin tipos: los define la conversación")
	assert.False(t, tenant.OnboardingCompleted, "Vendi debe correr tras el login (AC-01)")
	assert.NotEmpty(t, tenant.SaleTypes, "sale_types con default sensato (columna not null)")
	require.NotNil(t, tenant.DataNoticeAcceptedAt, "aviso de datos registrado (AC-15/FR-13)")
	assert.Equal(t, models.CatalogTermsVersion, tenant.TermsAcceptedVersion)
}

func TestTenantRegisterMinimalWithoutDataNotice(t *testing.T) {
	// El aviso es informativo (no fail-closed como los términos): si el
	// cliente no lo reporta, simplemente no se registra la marca.
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	body := map[string]any{
		"owner":        map[string]any{"name": "Bryan", "phone": phone, "password": "1234"},
		"accept_terms": true,
	}
	raw, _ := json.Marshal(body)
	w := postRaw(setupRouter(db), raw)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var tenant models.Tenant
	require.NoError(t, db.Where("phone = ?", phone).First(&tenant).Error)
	assert.Nil(t, tenant.DataNoticeAcceptedAt)
}

func TestTenantRegisterFullLegacyPayloadStillWorks(t *testing.T) {
	// Art. X: apps viejas mandan el payload completo — comportamiento intacto.
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	w := postJSON(setupRouter(db), defaultPayload(phone))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var tenant models.Tenant
	require.NoError(t, db.Where("phone = ?", phone).First(&tenant).Error)
	assert.NotEqual(t, "Mi negocio", tenant.BusinessName, "payload completo conserva su nombre")
	assert.NotEmpty(t, tenant.BusinessTypes)
	assert.Nil(t, tenant.DataNoticeAcceptedAt, "campo nuevo no inventado para apps viejas")
}

func TestTenantRegisterMinimalStillFailClosedOnTerms(t *testing.T) {
	// Spec 098 sigue mandando: sin términos no hay cuenta, ni en el flujo corto.
	db := setupTestDB(t)
	phone := uniquePhone()

	body := map[string]any{
		"owner":                map[string]any{"name": "Bryan", "phone": phone, "password": "1234"},
		"data_notice_accepted": true,
	}
	raw, _ := json.Marshal(body)
	w := postRaw(setupRouter(db), raw)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// postRaw envía un cuerpo JSON ya serializado al endpoint de registro.
func postRaw(r *gin.Engine, raw []byte) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/tenant/register", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}
