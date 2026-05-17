// Spec: specs/008-planes-suscripcion-epayco/spec.md
package services

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"vendia-backend/internal/billing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testEpaycoCreds are fake credentials — ePayco is mocked in tests, no
// real keys ever touch `go test` (plan §6). The signature math is
// deterministic so we can compute the expected hash ourselves.
func testEpaycoSvc() *EpaycoService {
	return NewEpaycoService(EpaycoConfig{
		PublicKey:  "pub_test_xxx",
		PrivateKey: "priv_test_xxx",
		PCustID:    "1234567",
		PKey:       "p_key_secret_abc",
		TestMode:   true,
	})
}

// expectedSignature mirrors the documented ePayco formula so the test
// is independent of the implementation under test.
func expectedSignature(pCustID, pKey, refPayco, txID, amount, currency string) string {
	raw := strings.Join([]string{pCustID, pKey, refPayco, txID, amount, currency}, "^")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func TestEpaycoService_NilSafe(t *testing.T) {
	var svc *EpaycoService
	assert.False(t, svc.IsConfigured(), "un servicio nil no esta configurado")
}

func TestEpaycoService_IsConfigured(t *testing.T) {
	configured := testEpaycoSvc()
	assert.True(t, configured.IsConfigured())

	missing := NewEpaycoService(EpaycoConfig{})
	assert.False(t, missing.IsConfigured(), "sin credenciales no esta configurado")

	partial := NewEpaycoService(EpaycoConfig{PublicKey: "x", PrivateKey: "y"})
	assert.False(t, partial.IsConfigured(), "faltando P_CUST_ID/P_KEY no esta configurado")
}

func TestEpaycoService_VerifySignature_Valid(t *testing.T) {
	svc := testEpaycoSvc()

	conf := EpaycoConfirmation{
		RefPayco:      "ref-abc-123",
		TransactionID: "tx-998877",
		Amount:        "29900.00",
		CurrencyCode:  "COP",
	}
	conf.Signature = expectedSignature("1234567", "p_key_secret_abc",
		"ref-abc-123", "tx-998877", "29900.00", "COP")

	assert.True(t, svc.VerifySignature(conf), "una firma correcta debe verificar")
}

func TestEpaycoService_VerifySignature_Invalid(t *testing.T) {
	svc := testEpaycoSvc()

	conf := EpaycoConfirmation{
		RefPayco:      "ref-abc-123",
		TransactionID: "tx-998877",
		Amount:        "29900.00",
		CurrencyCode:  "COP",
		Signature:     "deadbeefdeadbeefdeadbeef", // firma falsificada
	}

	assert.False(t, svc.VerifySignature(conf), "una firma falsa NO debe verificar (AC-06)")
}

func TestEpaycoService_VerifySignature_TamperedAmount(t *testing.T) {
	svc := testEpaycoSvc()

	// Firma valida para 29.900, pero el atacante cambia el monto a 1.
	conf := EpaycoConfirmation{
		RefPayco:      "ref-abc-123",
		TransactionID: "tx-998877",
		Amount:        "1.00",
		CurrencyCode:  "COP",
		Signature: expectedSignature("1234567", "p_key_secret_abc",
			"ref-abc-123", "tx-998877", "29900.00", "COP"),
	}

	assert.False(t, svc.VerifySignature(conf),
		"cambiar el monto invalida la firma (anti-tampering)")
}

func TestEpaycoService_VerifySignature_EmptySignatureRejected(t *testing.T) {
	svc := testEpaycoSvc()
	conf := EpaycoConfirmation{
		RefPayco:      "ref-abc-123",
		TransactionID: "tx-998877",
		Amount:        "29900.00",
		CurrencyCode:  "COP",
		Signature:     "",
	}
	assert.False(t, svc.VerifySignature(conf), "una firma vacia se rechaza")
}

func TestEpaycoService_VerifySignature_NilServiceRejects(t *testing.T) {
	var svc *EpaycoService
	assert.False(t, svc.VerifySignature(EpaycoConfirmation{Signature: "anything"}))
}

func TestEpaycoConfirmation_IsAccepted(t *testing.T) {
	// x_cod_response == 1 → aceptada.
	assert.True(t, EpaycoConfirmation{CodResponse: "1"}.IsAccepted())
	assert.False(t, EpaycoConfirmation{CodResponse: "2"}.IsAccepted(), "rechazada")
	assert.False(t, EpaycoConfirmation{CodResponse: "3"}.IsAccepted(), "pendiente")
	assert.False(t, EpaycoConfirmation{CodResponse: ""}.IsAccepted())
}

func TestEpaycoService_BuildCheckout_ProMonthly(t *testing.T) {
	svc := testEpaycoSvc()
	price, err := billing.LookupPrice(billing.PlanPro, billing.IntervalMonthly)
	require.NoError(t, err)

	co := svc.BuildCheckout(CheckoutParams{
		TenantID:        "tenant-xyz",
		Plan:            billing.PlanPro,
		Interval:        billing.IntervalMonthly,
		Price:           price,
		Reference:       "vendia-sub-tenant-xyz-001",
		ResponseURL:     "https://api.vendia.store/api/v1/subscription/response",
		ConfirmationURL: "https://api.vendia.store/api/v1/subscription/epayco/confirmation",
	})

	assert.Equal(t, "pub_test_xxx", co.PublicKey)
	assert.Equal(t, "vendia-sub-tenant-xyz-001", co.Invoice)
	assert.Equal(t, "29900", co.Amount, "monto en COP entero, sin decimales")
	assert.Equal(t, "COP", co.Currency)
	assert.True(t, co.Test, "TestMode=true → checkout en sandbox")
	assert.NotEmpty(t, co.Name)
	assert.NotEmpty(t, co.Description)
	assert.Contains(t, co.Description, "mensual")
	assert.Equal(t, "https://api.vendia.store/api/v1/subscription/response", co.Response)
	assert.Equal(t, "https://api.vendia.store/api/v1/subscription/epayco/confirmation", co.Confirmation)
	assert.Equal(t, "tenant-xyz", co.Extra1, "extra1 lleva el tenant_id para reconciliacion")
	assert.Equal(t, billing.PlanPro, co.Extra2)
	assert.Equal(t, billing.IntervalMonthly, co.Extra3)
}

func TestEpaycoService_BuildCheckout_ProYearlyDescription(t *testing.T) {
	svc := testEpaycoSvc()
	price, _ := billing.LookupPrice(billing.PlanPro, billing.IntervalYearly)

	co := svc.BuildCheckout(CheckoutParams{
		TenantID:  "t1",
		Plan:      billing.PlanPro,
		Interval:  billing.IntervalYearly,
		Price:     price,
		Reference: "ref-1",
	})
	assert.Equal(t, "299000", co.Amount)
	assert.Contains(t, co.Description, "anual")
}

func TestEpaycoService_BuildCheckout_ProductionMode(t *testing.T) {
	svc := NewEpaycoService(EpaycoConfig{
		PublicKey: "pub", PrivateKey: "priv", PCustID: "1", PKey: "k",
		TestMode: false,
	})
	price, _ := billing.LookupPrice(billing.PlanPro, billing.IntervalMonthly)
	co := svc.BuildCheckout(CheckoutParams{
		TenantID: "t", Plan: billing.PlanPro, Interval: billing.IntervalMonthly,
		Price: price, Reference: "r",
	})
	assert.False(t, co.Test, "TestMode=false → checkout en produccion")
}

func TestEpaycoService_GenerateReference_UniqueAndTenantScoped(t *testing.T) {
	svc := testEpaycoSvc()
	r1 := svc.GenerateReference("tenant-aaa")
	r2 := svc.GenerateReference("tenant-aaa")

	assert.NotEqual(t, r1, r2, "dos referencias del mismo tenant deben diferir")
	assert.Contains(t, r1, "tenant-aaa", "la referencia incluye el tenant_id")
	assert.True(t, strings.HasPrefix(r1, "vendia-sub-"), "prefijo identificable")
}

// ── RenderCheckoutPage ──────────────────────────────────────────────
//
// The backend-served checkout page is what reconciles the F008
// mismatch: the Flutter app cannot host the ePayco JS widget, so the
// backend serves an HTML page that loads checkout.js and opens the
// widget itself. /subscription/pay/:ref renders this page.

func TestEpaycoService_RenderCheckoutPage_EmbedsWidgetAndParams(t *testing.T) {
	svc := testEpaycoSvc()
	price, _ := billing.LookupPrice(billing.PlanPro, billing.IntervalMonthly)
	co := svc.BuildCheckout(CheckoutParams{
		TenantID:        "tenant-xyz",
		Plan:            billing.PlanPro,
		Interval:        billing.IntervalMonthly,
		Price:           price,
		Reference:       "vendia-sub-tenant-xyz-001",
		ResponseURL:     "https://api.vendia.store/api/v1/subscription/response",
		ConfirmationURL: "https://api.vendia.store/api/v1/subscription/epayco/confirmation",
	})

	html := svc.RenderCheckoutPage(co)

	// Carga el widget oficial de ePayco.
	assert.Contains(t, html, "https://checkout.epayco.co/checkout.js",
		"la pagina carga el script oficial del widget de ePayco")
	assert.Contains(t, html, "ePayco.checkout.configure",
		"configura el handler del checkout")
	assert.Contains(t, html, ".open(",
		"abre el widget de ePayco")

	// Lleva los parametros de esta referencia.
	assert.Contains(t, html, "pub_test_xxx", "incrusta la llave publica")
	assert.Contains(t, html, "vendia-sub-tenant-xyz-001",
		"incrusta la referencia como invoice")
	assert.Contains(t, html, "29900", "incrusta el monto")
	assert.Contains(t, html, "tenant-xyz", "incrusta el tenant_id en extra1")
	assert.Contains(t, html, "/api/v1/subscription/response",
		"incrusta la URL de respuesta")
	assert.Contains(t, html, "/api/v1/subscription/epayco/confirmation",
		"incrusta la URL de confirmacion")
	assert.Contains(t, html, "COP")
	assert.Contains(t, html, `lang="es"`, "la pagina esta en espanol")
}

func TestEpaycoService_RenderCheckoutPage_TestModeFlag(t *testing.T) {
	test := testEpaycoSvc() // TestMode: true
	prod := NewEpaycoService(EpaycoConfig{
		PublicKey: "pub", PrivateKey: "priv", PCustID: "1", PKey: "k",
		TestMode: false,
	})
	price, _ := billing.LookupPrice(billing.PlanPro, billing.IntervalMonthly)

	testHTML := test.RenderCheckoutPage(test.BuildCheckout(CheckoutParams{
		TenantID: "t", Plan: billing.PlanPro, Interval: billing.IntervalMonthly,
		Price: price, Reference: "r-test",
	}))
	prodHTML := prod.RenderCheckoutPage(prod.BuildCheckout(CheckoutParams{
		TenantID: "t", Plan: billing.PlanPro, Interval: billing.IntervalMonthly,
		Price: price, Reference: "r-prod",
	}))

	assert.Contains(t, testHTML, "test: true",
		"TestMode=true → widget en sandbox")
	assert.Contains(t, prodHTML, "test: false",
		"TestMode=false → widget en produccion")
}

// La referencia / descripcion se escapan en JS para que un valor con
// comillas no pueda romper el script o inyectar codigo.
func TestEpaycoService_RenderCheckoutPage_EscapesParams(t *testing.T) {
	svc := testEpaycoSvc()
	price, _ := billing.LookupPrice(billing.PlanPro, billing.IntervalMonthly)
	co := svc.BuildCheckout(CheckoutParams{
		TenantID:  `t"</script><script>alert(1)</script>`,
		Plan:      billing.PlanPro,
		Interval:  billing.IntervalMonthly,
		Price:     price,
		Reference: `ref"injected`,
	})

	html := svc.RenderCheckoutPage(co)
	assert.NotContains(t, html, "<script>alert(1)</script>",
		"un valor malicioso no se inyecta sin escapar")
}
