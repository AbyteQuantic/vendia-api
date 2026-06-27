// Spec: specs/086-branding-estacional/spec.md
package services

import (
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
)

func tp(s string) *time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return &t
}

func TestResolveActiveCampaign(t *testing.T) {
	now := mustTime("2026-12-25T12:00:00Z")
	dec := models.SeasonalCampaign{
		Key: "navidad", Enabled: true,
		StartsAt: tp("2026-12-01T00:00:00Z"), EndsAt: tp("2026-12-26T00:00:00Z"),
	}

	t.Run("dentro de ventana + enabled → activa", func(t *testing.T) {
		got, ok := ResolveActiveCampaign([]models.SeasonalCampaign{dec}, now)
		assert.True(t, ok)
		assert.Equal(t, "navidad", got.Key)
	})

	t.Run("fuera de ventana → inactiva", func(t *testing.T) {
		_, ok := ResolveActiveCampaign([]models.SeasonalCampaign{dec}, mustTime("2026-06-01T12:00:00Z"))
		assert.False(t, ok)
	})

	t.Run("enabled=false en ventana → inactiva", func(t *testing.T) {
		off := dec
		off.Enabled = false
		_, ok := ResolveActiveCampaign([]models.SeasonalCampaign{off}, now)
		assert.False(t, ok)
	})

	t.Run("force_active fuera de rango → activa", func(t *testing.T) {
		f := dec
		f.ForceActive = true
		got, ok := ResolveActiveCampaign([]models.SeasonalCampaign{f}, mustTime("2026-06-01T12:00:00Z"))
		assert.True(t, ok)
		assert.Equal(t, "navidad", got.Key)
	})

	t.Run("EndsAt es EXCLUSIVO", func(t *testing.T) {
		_, ok := ResolveActiveCampaign([]models.SeasonalCampaign{dec}, mustTime("2026-12-26T00:00:00Z"))
		assert.False(t, ok, "now == EndsAt queda fuera")
	})

	t.Run("dos solapadas → gana mayor Priority", func(t *testing.T) {
		a := dec
		a.Key = "a"
		a.Priority = 1
		b := dec
		b.Key = "b"
		b.Priority = 5
		got, ok := ResolveActiveCampaign([]models.SeasonalCampaign{a, b}, now)
		assert.True(t, ok)
		assert.Equal(t, "b", got.Key)
	})

	t.Run("límites nil → sin restricción", func(t *testing.T) {
		open := models.SeasonalCampaign{Key: "siempre", Enabled: true}
		got, ok := ResolveActiveCampaign([]models.SeasonalCampaign{open}, now)
		assert.True(t, ok)
		assert.Equal(t, "siempre", got.Key)
	})

	t.Run("sin filas → inactiva", func(t *testing.T) {
		_, ok := ResolveActiveCampaign(nil, now)
		assert.False(t, ok)
	})
}

func mustTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
