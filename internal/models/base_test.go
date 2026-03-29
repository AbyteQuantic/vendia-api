package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsValidUUID_ValidFormats(t *testing.T) {
	assert.True(t, IsValidUUID("550e8400-e29b-41d4-a716-446655440000"))
	assert.True(t, IsValidUUID("6ba7b810-9dad-11d1-80b4-00c04fd430c8"))
}

func TestIsValidUUID_InvalidFormats(t *testing.T) {
	assert.False(t, IsValidUUID("not-a-uuid"))
	assert.False(t, IsValidUUID(""))
	assert.False(t, IsValidUUID("123"))
	assert.False(t, IsValidUUID("550e8400-e29b-41d4-a716"))
}
