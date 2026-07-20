// Spec: specs/107-dashboard-v2-resumen/spec.md (FR-08b/c/d)
package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeAgentActionWhitelist(t *testing.T) {
	// navigate con ruta conocida → pasa
	a := SanitizeAgentAction(&AgentAssistAction{Type: "navigate",
		Params: map[string]string{"route": "fiados"}})
	require.NotNil(t, a)
	assert.Equal(t, "fiados", a.Params["route"])

	// navigate con ruta desconocida → nil (AC-08d)
	assert.Nil(t, SanitizeAgentAction(&AgentAssistAction{Type: "navigate",
		Params: map[string]string{"route": "'; DROP TABLE tenants;--"}}))

	// tipo fuera del catálogo → nil
	assert.Nil(t, SanitizeAgentAction(&AgentAssistAction{Type: "delete_all"}))
	assert.Nil(t, SanitizeAgentAction(&AgentAssistAction{Type: "register_sale",
		Params: map[string]string{"total": "5000"}}))
	assert.Nil(t, SanitizeAgentAction(nil))
}

func TestSanitizeAgentActionCreateProduct(t *testing.T) {
	ok := SanitizeAgentAction(&AgentAssistAction{Type: "create_product",
		Params: map[string]string{"name": "Arroz Diana", "price": "3500", "stock": "12"}})
	require.NotNil(t, ok)
	assert.Equal(t, "Arroz Diana", ok.Params["name"])
	assert.Equal(t, "3500", ok.Params["price"])

	// precio inválido o <= 0 → nil (Art. VII)
	assert.Nil(t, SanitizeAgentAction(&AgentAssistAction{Type: "create_product",
		Params: map[string]string{"name": "X", "price": "0"}}))
	assert.Nil(t, SanitizeAgentAction(&AgentAssistAction{Type: "create_product",
		Params: map[string]string{"name": "Arroz", "price": "abc"}}))
	// nombre demasiado corto → nil
	assert.Nil(t, SanitizeAgentAction(&AgentAssistAction{Type: "create_product",
		Params: map[string]string{"name": "A", "price": "1000"}}))
	// stock negativo → nil
	assert.Nil(t, SanitizeAgentAction(&AgentAssistAction{Type: "create_product",
		Params: map[string]string{"name": "Arroz", "price": "1000", "stock": "-3"}}))
}

func TestSanitizeAgentActionCreateCustomer(t *testing.T) {
	ok := SanitizeAgentAction(&AgentAssistAction{Type: "create_customer",
		Params: map[string]string{"name": "María Gómez", "phone": "300 123 4567"}})
	require.NotNil(t, ok)
	assert.Equal(t, "3001234567", ok.Params["phone"], "teléfono normalizado a dígitos")

	// sin teléfono también vale (opcional)
	ok2 := SanitizeAgentAction(&AgentAssistAction{Type: "create_customer",
		Params: map[string]string{"name": "Pedro"}})
	require.NotNil(t, ok2)

	assert.Nil(t, SanitizeAgentAction(&AgentAssistAction{Type: "create_customer",
		Params: map[string]string{"name": "P"}}))
}

func TestAssistActionSummarySpanish(t *testing.T) {
	s := AssistActionSummary(&AgentAssistAction{Type: "create_product",
		Params: map[string]string{"name": "Arroz Diana", "price": "3500", "stock": "12"}})
	assert.Contains(t, s, "Arroz Diana")
	assert.Contains(t, s, "3.500")
	s2 := AssistActionSummary(&AgentAssistAction{Type: "navigate",
		Params: map[string]string{"route": "fiados"}})
	assert.NotEmpty(t, s2)
}
