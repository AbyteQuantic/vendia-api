package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOCRService_RegexExtractsNIT(t *testing.T) {
	svc := NewOCRService("")
	text := "NIT: 900.123.456-7\nTotal: $150,000\nFecha: 22/03/2026\nProductos varios"

	result := svc.tryRegex(text)
	require.NotNil(t, result, "regex should extract 3+ fields")
	assert.Equal(t, "regex", result.Method)
	assert.Equal(t, "900.123.456-7", result.NIT)
	assert.Equal(t, "150,000", result.Total)
	assert.Equal(t, "22/03/2026", result.Date)
}

func TestOCRService_RegexReturnsNilWhenInsufficient(t *testing.T) {
	svc := NewOCRService("")
	text := "Some random text without invoice data"

	result := svc.tryRegex(text)
	assert.Nil(t, result, "regex should return nil with < 3 fields")
}

func TestOCRService_ProcessInvoiceWithoutAPIKey(t *testing.T) {
	svc := NewOCRService("")
	result, err := svc.ProcessInvoice([]byte("random image data"), "image/jpeg")
	require.NoError(t, err)
	assert.Equal(t, "regex_partial", result.Method)
}

func TestOCRService_RegexNITFormats(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"NIT 900123456-7", "900123456-7"},
		{"900.123.456-7", "900.123.456-7"},
		{"Nit: 800999888-1", "800999888-1"},
	}
	for _, tc := range cases {
		m := nitRegex.FindString(tc.input)
		assert.Equal(t, tc.expected, m, "input: %s", tc.input)
	}
}
