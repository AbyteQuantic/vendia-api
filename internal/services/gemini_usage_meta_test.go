package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGemUsageMetadata_InputOutput_camelCase(t *testing.T) {
	m := GemUsageMetadata{
		PromptTokenCount:     100,
		CandidatesTokenCount: 50,
	}
	in, out := m.InputOutput()
	assert.Equal(t, 100, in)
	assert.Equal(t, 50, out)
}

func TestGemUsageMetadata_InputOutput_snakeCaseFallback(t *testing.T) {
	m := GemUsageMetadata{
		PromptTokenCountAlt: 10,
		CandidatesTokenAlt:  20,
	}
	in, out := m.InputOutput()
	assert.Equal(t, 10, in)
	assert.Equal(t, 20, out)
}

func TestGemUsageMetadata_InputOutput_totalOnly(t *testing.T) {
	m := GemUsageMetadata{TotalTokenCount: 999}
	in, out := m.InputOutput()
	assert.Equal(t, 0, in)
	assert.Equal(t, 999, out)
}
