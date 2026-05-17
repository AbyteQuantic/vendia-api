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
