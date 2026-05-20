// Spec: specs/026-importador-clientes/spec.md
package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizePhone(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "full international format with spaces",
			input:    "+57 300 123 4567",
			expected: "573001234567",
		},
		{
			name:     "format with dashes and parens",
			input:    "(300) 123-4567",
			expected: "3001234567",
		},
		{
			name:     "local format with dots",
			input:    "300.123.4567",
			expected: "3001234567",
		},
		{
			name:     "plain digits already normalized",
			input:    "3001234567",
			expected: "3001234567",
		},
		{
			name:     "too few digits returns empty",
			input:    "12",
			expected: "",
		},
		{
			name:     "alphabetic only returns empty",
			input:    "abc",
			expected: "",
		},
		{
			name:     "empty string returns empty",
			input:    "",
			expected: "",
		},
		{
			name:     "exactly 7 digits is valid",
			input:    "1234567",
			expected: "1234567",
		},
		{
			name:     "6 digits returns empty",
			input:    "123456",
			expected: "",
		},
		{
			name:     "mixed letters and digits returns empty when result < 7",
			input:    "abc123",
			expected: "",
		},
		{
			name:     "spaces only returns empty",
			input:    "   ",
			expected: "",
		},
		{
			name:     "Colombian mobile with +57 prefix",
			input:    "+57 314 876 5432",
			expected: "573148765432",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizePhone(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}
