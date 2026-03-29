package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatCOP_Small(t *testing.T) {
	assert.Equal(t, "500", formatCOP(500))
}

func TestFormatCOP_Thousands(t *testing.T) {
	assert.Equal(t, "5.000", formatCOP(5000))
}

func TestFormatCOP_Millions(t *testing.T) {
	assert.Equal(t, "1.500.000", formatCOP(1500000))
}

func TestFormatCOP_TenThousands(t *testing.T) {
	assert.Equal(t, "10.000", formatCOP(10000))
}

func TestFormatCOP_HundredThousands(t *testing.T) {
	assert.Equal(t, "125.000", formatCOP(125000))
}

func TestWhatsAppService_ReceiptMessage(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.ReceiptMessage("Tienda Don Pepe", 5000, "https://vendia.co/receipt/123")
	assert.Contains(t, msg, "Tienda Don Pepe")
	assert.Contains(t, msg, "$5.000")
	assert.Contains(t, msg, "https://vendia.co/receipt/123")
}

func TestWhatsAppService_CreditHandshake(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.CreditHandshake("Carlos", "Tienda Don Pepe", 10000)
	assert.Contains(t, msg, "Carlos")
	assert.Contains(t, msg, "Tienda Don Pepe")
	assert.Contains(t, msg, "$10.000")
	assert.Contains(t, msg, "fiado")
}

func TestWhatsAppService_CreditReminder(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.CreditReminder("Carlos", "Tienda Don Pepe", 15000)
	assert.Contains(t, msg, "Carlos")
	assert.Contains(t, msg, "saldo pendiente")
	assert.Contains(t, msg, "$15.000")
}

func TestWhatsAppService_SupplierOrder(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.SupplierOrder("Pedro", "Coca-Cola 350ml", 24, "Don Pepe")
	assert.Contains(t, msg, "Pedro")
	assert.Contains(t, msg, "24 unidades")
	assert.Contains(t, msg, "Coca-Cola 350ml")
	assert.Contains(t, msg, "Don Pepe")
}

func TestWhatsAppService_BuildURL_ColombianNumber(t *testing.T) {
	svc := NewWhatsAppService()
	url := svc.BuildURL("3001234567", "Hola mundo")
	assert.Contains(t, url, "https://wa.me/573001234567")
	assert.Contains(t, url, "text=Hola")
}

func TestWhatsAppService_BuildURL_WithCountryCode(t *testing.T) {
	svc := NewWhatsAppService()
	url := svc.BuildURL("+573001234567", "Hola")
	assert.Contains(t, url, "wa.me/573001234567")
}

func TestWhatsAppService_BuildURL_AlreadyFormatted(t *testing.T) {
	svc := NewWhatsAppService()
	url := svc.BuildURL("573001234567", "Test")
	assert.Contains(t, url, "wa.me/573001234567")
}

func TestWhatsAppService_BuildURL_SpecialChars(t *testing.T) {
	svc := NewWhatsAppService()
	url := svc.BuildURL("3001234567", "Hola! ¿Cómo estás?")
	assert.Contains(t, url, "wa.me/573001234567")
	assert.Contains(t, url, "text=")
}
