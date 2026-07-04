// Spec: specs/095-variantes-producto/spec.md
package handlers

import (
	"testing"
	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
)

func TestApplyCapabilityFlags_ProductVariants(t *testing.T) {
	on := models.Tenant{EnableProductVariants: true}
	resp := &AuthResponse{}
	applyCapabilityFlags(resp, on)
	assert.True(t, resp.EnableProductVariants)

	off := models.Tenant{EnableProductVariants: false}
	resp2 := &AuthResponse{}
	applyCapabilityFlags(resp2, off)
	assert.False(t, resp2.EnableProductVariants)
}
