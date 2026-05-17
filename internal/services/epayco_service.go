// Spec: specs/008-planes-suscripcion-epayco/spec.md
//
// EpaycoService integrates the ePayco payment gateway for VendIA's
// PRO subscription (Feature 008). It owns two responsibilities:
//
//  1. Build the data the frontend needs to open the ePayco checkout
//     widget for a {plan, interval}.
//  2. Verify the SHA-256 signature on the confirmation webhook so a
//     forged callback can never promote a tenant to PRO (Art. VI).
//
// The service holds NO network client: v1 does not call ePayco's REST
// API server-side — the checkout runs in the browser widget and the
// confirmation arrives as a webhook. That keeps `go test` fully
// offline and the service trivially mockable (plan §6).
package services

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"vendia-backend/internal/billing"

	"github.com/google/uuid"
)

// EpaycoConfig carries the gateway credentials read from env vars in
// internal/config. Never logged, never hardcoded (Art. VI / D5).
type EpaycoConfig struct {
	PublicKey  string // EPAYCO_PUBLIC_KEY — widget public key
	PrivateKey string // EPAYCO_PRIVATE_KEY — server-side private key
	PCustID    string // EPAYCO_P_CUST_ID — p_cust_id_cliente
	PKey       string // EPAYCO_P_KEY — signature secret
	TestMode   bool   // EPAYCO_TEST_MODE — sandbox vs production
}

// EpaycoService verifies confirmations and builds checkout payloads.
type EpaycoService struct {
	cfg EpaycoConfig
}

// NewEpaycoService builds the service. Always returns a non-nil value
// so the handler layer never has to nil-check the constructor — use
// IsConfigured to gate behaviour when credentials are absent.
func NewEpaycoService(cfg EpaycoConfig) *EpaycoService {
	return &EpaycoService{cfg: cfg}
}

// IsConfigured reports whether all four credentials are present. The
// checkout/webhook handlers refuse to operate when this is false so a
// misconfigured deploy fails loudly instead of charging into ePayco
// with empty keys. Nil-safe.
func (s *EpaycoService) IsConfigured() bool {
	if s == nil {
		return false
	}
	return s.cfg.PublicKey != "" &&
		s.cfg.PrivateKey != "" &&
		s.cfg.PCustID != "" &&
		s.cfg.PKey != ""
}

// EpaycoConfirmation is the subset of fields the confirmation webhook
// carries that VendIA needs. ePayco posts ~30 fields; these are the
// ones that feed signature verification, the accept/reject decision
// and the payment record. Field names mirror ePayco's x_* form keys.
type EpaycoConfirmation struct {
	RefPayco      string // x_ref_payco — ePayco's payment reference
	TransactionID string // x_transaction_id — idempotency key
	Amount        string // x_amount — charged amount, as ePayco sent it
	CurrencyCode  string // x_currency_code — e.g. "COP"
	Signature     string // x_signature — SHA-256 hash to verify
	CodResponse   string // x_cod_response — "1" = accepted
	ResponseText  string // x_response_reason_text — human-readable status
	Invoice       string // x_id_invoice — our own checkout reference
	Extra1        string // tenant_id (set by BuildCheckout)
	Extra2        string // plan id (set by BuildCheckout)
	Extra3        string // interval (set by BuildCheckout)
}

// IsAccepted reports whether ePayco accepted the transaction. ePayco's
// x_cod_response: 1 = aceptada, 2 = rechazada, 3 = pendiente, 4 = fallida.
// Only "1" promotes a tenant (FR-05).
func (c EpaycoConfirmation) IsAccepted() bool {
	return c.CodResponse == "1"
}

// VerifySignature recomputes the ePayco signature and compares it,
// in constant time, against the x_signature the webhook carried.
//
// Formula (ePayco docs):
//
//	SHA256(p_cust_id_cliente ^ p_key ^ x_ref_payco ^
//	       x_transaction_id ^ x_amount ^ x_currency_code)
//
// A nil service, an unconfigured service, or an empty signature all
// return false — the webhook is public, so an unverifiable payload is
// never trusted (Art. VI / AC-06).
func (s *EpaycoService) VerifySignature(c EpaycoConfirmation) bool {
	if s == nil || !s.IsConfigured() || c.Signature == "" {
		return false
	}
	expected := s.computeSignature(c)
	// Constant-time compare avoids leaking byte-match progress to a
	// timing attacker probing the public webhook.
	return subtle.ConstantTimeCompare(
		[]byte(strings.ToLower(expected)),
		[]byte(strings.ToLower(c.Signature)),
	) == 1
}

// computeSignature builds the SHA-256 hex digest per ePayco's formula.
func (s *EpaycoService) computeSignature(c EpaycoConfirmation) string {
	raw := strings.Join([]string{
		s.cfg.PCustID,
		s.cfg.PKey,
		c.RefPayco,
		c.TransactionID,
		c.Amount,
		c.CurrencyCode,
	}, "^")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CheckoutParams is the input to BuildCheckout — everything needed to
// describe one subscription purchase.
type CheckoutParams struct {
	TenantID        string
	Plan            string
	Interval        string
	Price           billing.Price
	Reference       string // unique checkout reference (x_id_invoice)
	ResponseURL     string // browser landing after payment
	ConfirmationURL string // server webhook ePayco posts to
}

// EpaycoCheckout is the payload the frontend feeds to the ePayco
// widget. JSON tags use ePayco's widget field names so the Flutter /
// web client can hand it straight to the SDK.
type EpaycoCheckout struct {
	PublicKey    string `json:"key"`
	Test         bool   `json:"test"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Invoice      string `json:"invoice"`
	Amount       string `json:"amount"`
	TaxBase      string `json:"tax_base"`
	Tax          string `json:"tax"`
	Currency     string `json:"currency"`
	Country      string `json:"country"`
	Lang         string `json:"lang"`
	Response     string `json:"response"`
	Confirmation string `json:"confirmation"`
	Extra1       string `json:"extra1"`
	Extra2       string `json:"extra2"`
	Extra3       string `json:"extra3"`
}

// BuildCheckout assembles the checkout payload for a subscription
// purchase. The amount is rendered as an integer COP string (Art. VII
// — money is exact, no floating point). extra1/2/3 carry tenant_id /
// plan / interval so the confirmation webhook can reconcile the
// payment back to the right tenant even before looking up the row.
func (s *EpaycoService) BuildCheckout(p CheckoutParams) EpaycoCheckout {
	intervalLabel := "mensual"
	if p.Interval == billing.IntervalYearly {
		intervalLabel = "anual"
	}
	amount := strconv.Itoa(p.Price.Amount)

	pubKey := ""
	if s != nil {
		pubKey = s.cfg.PublicKey
	}
	test := true
	if s != nil {
		test = s.cfg.TestMode
	}

	return EpaycoCheckout{
		PublicKey:    pubKey,
		Test:         test,
		Name:         "VendIA Pro",
		Description:  fmt.Sprintf("Suscripción VendIA Pro (%s)", intervalLabel),
		Invoice:      p.Reference,
		Amount:       amount,
		TaxBase:      "0",
		Tax:          "0",
		Currency:     p.Price.Currency,
		Country:      "CO",
		Lang:         "es",
		Response:     p.ResponseURL,
		Confirmation: p.ConfirmationURL,
		Extra1:       p.TenantID,
		Extra2:       p.Plan,
		Extra3:       p.Interval,
	}
}

// GenerateReference builds a unique, tenant-scoped checkout reference.
// Shape: vendia-sub-<tenantID>-<unixnano>-<short-uuid>. The tenant id
// keeps it human-readable for reconciliation; the nanosecond timestamp
// plus a uuid fragment guarantee uniqueness even on the same tick.
func (s *EpaycoService) GenerateReference(tenantID string) string {
	short := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	return fmt.Sprintf("vendia-sub-%s-%d-%s",
		tenantID, time.Now().UnixNano(), short)
}

// RenderCheckoutPage builds the self-contained HTML page the backend
// SERVES so the ePayco checkout actually opens.
//
// Why the backend serves a page (the F008 reconciliation):
//
//	The Flutter app — web and mobile — has no place to host ePayco's
//	browser-only JS widget. So /subscription/pay/:ref returns this
//	page: it loads the official widget (checkout.js) and calls
//	ePayco.checkout.configure({key,test}).open({...}) with the params
//	of that reference. The Flutter CTA just opens the page URL with
//	launchUrl — the page does the rest.
//
// Every dynamic value is injected as a JSON literal (encodeJSValue):
// JSON escaping makes a string with quotes / </script> a harmless
// string literal instead of an injection vector. The page is in
// Spanish (Art. V).
func (s *EpaycoService) RenderCheckoutPage(co EpaycoCheckout) string {
	test := "false"
	if co.Test {
		test = "true"
	}
	return `<!DOCTYPE html>
<html lang="es">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>VendIA — Pago de suscripción</title>
<style>
body{font-family:system-ui,sans-serif;background:#f4f5f7;margin:0;
display:flex;align-items:center;justify-content:center;min-height:100vh}
.card{background:#fff;border-radius:16px;padding:32px;max-width:360px;
text-align:center;box-shadow:0 8px 24px rgba(0,0,0,.08)}
h1{font-size:22px;color:#1a1a1a;margin:8px 0}
p{font-size:16px;color:#444;line-height:1.5}
.btn{display:inline-block;margin-top:16px;padding:14px 24px;
background:#1a7f37;color:#fff;border:none;border-radius:12px;
font-size:17px;font-weight:600;cursor:pointer}
.amount{font-size:20px;font-weight:700;color:#1a7f37;margin:4px 0}
</style>
</head>
<body>
<div class="card">
<h1>VendIA Pro</h1>
<p class="amount">$` + co.Amount + ` ` + co.Currency + `</p>
<p>Estamos abriendo la pasarela de pago segura de ePayco. Si no se
abre sola, toca el botón.</p>
<button class="btn" id="pagar" type="button">Pagar ahora</button>
</div>
<script src="https://checkout.epayco.co/checkout.js"></script>
<script>
var handler = ePayco.checkout.configure({
  key: ` + encodeJSValue(co.PublicKey) + `,
  test: ` + test + `
});
function abrirCheckout(){
  handler.open({
    name: ` + encodeJSValue(co.Name) + `,
    description: ` + encodeJSValue(co.Description) + `,
    invoice: ` + encodeJSValue(co.Invoice) + `,
    amount: ` + encodeJSValue(co.Amount) + `,
    tax_base: ` + encodeJSValue(co.TaxBase) + `,
    tax: ` + encodeJSValue(co.Tax) + `,
    currency: ` + encodeJSValue(co.Currency) + `,
    country: ` + encodeJSValue(co.Country) + `,
    lang: ` + encodeJSValue(co.Lang) + `,
    external: "false",
    response: ` + encodeJSValue(co.Response) + `,
    confirmation: ` + encodeJSValue(co.Confirmation) + `,
    extra1: ` + encodeJSValue(co.Extra1) + `,
    extra2: ` + encodeJSValue(co.Extra2) + `,
    extra3: ` + encodeJSValue(co.Extra3) + `
  });
}
document.getElementById("pagar").addEventListener("click", abrirCheckout);
abrirCheckout();
</script>
</body>
</html>`
}

// encodeJSValue renders v as a JSON literal safe to embed inside a
// <script> block: JSON marshaling escapes quotes and HTML-special
// characters, so a value carrying </script> or a quote becomes an inert
// string literal instead of breaking out of the script. Falls back to
// an empty string literal if marshaling ever fails (it cannot for a
// plain string, but never emit raw input).
func encodeJSValue(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	// json.Marshal escapes <, > and & to < etc., neutralising any
	// </script> inside the value.
	return string(b)
}
