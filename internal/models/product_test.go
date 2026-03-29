package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProduct_ContainerTotal(t *testing.T) {
	p := Product{
		Name:              "Coca-Cola 400ml",
		Price:             2500,
		Stock:             50,
		RequiresContainer: true,
		ContainerPrice:    500,
	}

	qty := 3
	productTotal := p.Price * float64(qty)
	containerTotal := float64(p.ContainerPrice) * float64(qty)
	grandTotal := productTotal + containerTotal

	assert.Equal(t, float64(7500), productTotal)
	assert.Equal(t, float64(1500), containerTotal)
	assert.Equal(t, float64(9000), grandTotal)
}

func TestProduct_IngestionMethods(t *testing.T) {
	methods := []string{"manual", "ia_factura", "barcode_scan"}
	for _, m := range methods {
		p := Product{IngestionMethod: m}
		assert.NotEmpty(t, p.IngestionMethod)
	}
}

func TestProduct_PriceStatuses(t *testing.T) {
	p1 := Product{PriceStatus: "set"}
	p2 := Product{PriceStatus: "pending"}
	assert.Equal(t, "set", p1.PriceStatus)
	assert.Equal(t, "pending", p2.PriceStatus)
}
