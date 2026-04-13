package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBusinessType_Constants(t *testing.T) {
	assert.Equal(t, "tienda_barrio", BusinessTypeTiendaBarrio)
	assert.Equal(t, "minimercado", BusinessTypeMinimercado)
	assert.Equal(t, "bar", BusinessTypeBar)
	assert.Equal(t, "miscelanea", BusinessTypeMiscelanea)
}

func TestTenant_StoreFields(t *testing.T) {
	slug := "don-pepe"
	tenant := Tenant{
		BusinessName:  "Tienda Don Pepe",
		BusinessTypes: []string{BusinessTypeTiendaBarrio},
		StoreSlug:     &slug,
		IsDeliveryOpen: true,
		DeliveryCost:   3000,
		MinOrderAmount: 10000,
		LogoURL:        "https://example.com/logo.webp",
	}

	assert.Equal(t, "don-pepe", *tenant.StoreSlug)
	assert.True(t, tenant.IsDeliveryOpen)
	assert.Equal(t, float64(3000), tenant.DeliveryCost)
	assert.Equal(t, float64(10000), tenant.MinOrderAmount)
	assert.NotEmpty(t, tenant.LogoURL)
	assert.Contains(t, tenant.BusinessTypes, BusinessTypeTiendaBarrio)
}

func TestTenant_ChargeModes(t *testing.T) {
	pre := Tenant{ChargeMode: "pre_payment"}
	post := Tenant{ChargeMode: "post_payment"}
	assert.Equal(t, "pre_payment", pre.ChargeMode)
	assert.Equal(t, "post_payment", post.ChargeMode)
}
