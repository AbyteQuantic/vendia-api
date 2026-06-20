package models_test

import (
	"github.com/stretchr/testify/assert"
	"testing"
	"vendia-backend/internal/models"
)

func TestSupplierModeFlag(t *testing.T) {
	f := models.DefaultFeatureFlags([]string{models.BusinessTypeProveedorAgricola}, models.CapabilityToggles{})
	assert.True(t, f.EnableSupplierMode)
	g := models.DefaultFeatureFlags([]string{models.BusinessTypeTiendaBarrio}, models.CapabilityToggles{})
	assert.False(t, g.EnableSupplierMode)
}
