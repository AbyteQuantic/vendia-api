package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeText_RemovesAccents(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"Café", "cafe"},
		{"Índice Único", "indice unico"},
		{"ÁGUILA León", "aguila leon"},
		{"piña colada", "pina colada"},
		{"jalapeño", "jalapeno"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.expected, NormalizeText(tt.input), "input: %s", tt.input)
	}
}

func TestNormalizeText_CollapsesWhitespace(t *testing.T) {
	assert.Equal(t, "coca cola 1.5l", NormalizeText("  Coca   Cola   1.5L  "))
}

func TestNormalizeText_LowercasesEverything(t *testing.T) {
	assert.Equal(t, "speed max 250ml", NormalizeText("SPEED MAX 250ML"))
}

func TestNormalizeText_EmptyInput(t *testing.T) {
	assert.Equal(t, "", NormalizeText(""))
	assert.Equal(t, "", NormalizeText("   "))
}

func TestNormalizeText_PreservesNumbers(t *testing.T) {
	assert.Equal(t, "agua cristal 600 ml pet x 24", NormalizeText("AGUA CRISTAL 600 ML PET X 24"))
}

func TestNormalizeText_DedupMatchScenario(t *testing.T) {
	// Two different representations of the same product should normalize identically
	a := NormalizeText("Coca-Cola 1.5L Botella")
	b := NormalizeText("coca-cola  1.5l   botella")
	assert.Equal(t, a, b)
}
