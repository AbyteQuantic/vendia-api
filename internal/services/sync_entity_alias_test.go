// Spec: specs/047-offline-sync-contract/spec.md (AC-06)
package services

import "testing"

func TestCanonicalSyncEntity(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"credit", "credit_account"},       // alias viejo → real
		{"credit_account", "credit_account"}, // ya canónico, intacto
		{"credit_payment", "credit_payment"}, // no se toca
		{"customer", "customer"},
		{"product", "product"},
		{"sale", "sale"},
		{"", ""},
	}
	for _, c := range cases {
		if got := canonicalSyncEntity(c.in); got != c.want {
			t.Errorf("canonicalSyncEntity(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
