package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOrderStatus_Constants(t *testing.T) {
	assert.Equal(t, OrderStatus("nuevo"), OrderStatusNuevo)
	assert.Equal(t, OrderStatus("preparando"), OrderStatusPreparando)
	assert.Equal(t, OrderStatus("listo"), OrderStatusListo)
	assert.Equal(t, OrderStatus("cobrado"), OrderStatusCobrado)
	assert.Equal(t, OrderStatus("cancelado"), OrderStatusCancelado)
}

func TestOrderType_Constants(t *testing.T) {
	assert.Equal(t, OrderType("mesa"), OrderTypeMesa)
	assert.Equal(t, OrderType("turno"), OrderTypeTurno)
	assert.Equal(t, OrderType("para_llevar"), OrderTypeParaLlevar)
	assert.Equal(t, OrderType("domicilio_web"), OrderTypeDomicilioWeb)
}

func TestOrderTicket_DefaultValues(t *testing.T) {
	ticket := OrderTicket{
		Label: "Mesa 1",
	}
	assert.Equal(t, "Mesa 1", ticket.Label)
	assert.Equal(t, float64(0), ticket.Total)
	assert.Empty(t, ticket.Items)
}

func TestOrderItem_Subtotal(t *testing.T) {
	item := OrderItem{
		ProductName: "Hamburguesa",
		Quantity:    2,
		UnitPrice:   12000,
	}
	subtotal := item.UnitPrice * float64(item.Quantity)
	assert.Equal(t, float64(24000), subtotal)
}
