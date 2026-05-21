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
	assert.Contains(t, msg, "$15.000")
	// The message uses "fiado pendiente de pago" in fiar mode (default).
	assert.Contains(t, msg, "fiado pendiente de pago")
}

func TestWhatsAppService_SupplierOrder(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.SupplierOrder("Pedro", "Coca-Cola 350ml", 24, "Don Pepe")
	assert.Contains(t, msg, "Pedro")
	assert.Contains(t, msg, "24 unidades")
	assert.Contains(t, msg, "Coca-Cola 350ml")
	assert.Contains(t, msg, "Don Pepe")
}

// T-07 / AC-02 (Feature 002) — the purchase-order WhatsApp message
// lists EVERY item with its quantity, plus the supplier and owner.
func TestWhatsAppService_PurchaseOrder_ListsAllItems(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.PurchaseOrder("Pedro", "Don Pepe", []PurchaseOrderLine{
		{Name: "Arroz", Quantity: 10, Unit: "kg"},
		{Name: "Coca-Cola 350ml", Quantity: 24, Unit: "unidad"},
		{Name: "Aceite", Quantity: 5, Unit: "l"},
	})
	assert.Contains(t, msg, "Pedro")
	assert.Contains(t, msg, "Don Pepe")
	assert.Contains(t, msg, "Arroz")
	assert.Contains(t, msg, "Coca-Cola 350ml")
	assert.Contains(t, msg, "Aceite")
	// Quantities must be present so the proveedor knows how much.
	assert.Contains(t, msg, "10")
	assert.Contains(t, msg, "24")
	assert.Contains(t, msg, "5")
}

// A fractional quantity (e.g. 2.5 kg) is rendered without a trailing
// ".0" but keeps real decimals.
func TestWhatsAppService_PurchaseOrder_FractionalQuantities(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.PurchaseOrder("Pedro", "Don Pepe", []PurchaseOrderLine{
		{Name: "Queso", Quantity: 2.5, Unit: "kg"},
		{Name: "Huevos", Quantity: 30, Unit: "unidad"},
	})
	assert.Contains(t, msg, "2.5")
	assert.Contains(t, msg, "Queso")
	assert.NotContains(t, msg, "30.0", "a whole quantity must not show a trailing .0")
	assert.Contains(t, msg, "30")
}

// An empty contact name is tolerated — the message still goes out
// (Art. I, cero fricción: never block on a missing optional field).
func TestWhatsAppService_PurchaseOrder_EmptyContactName(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.PurchaseOrder("", "Don Pepe", []PurchaseOrderLine{
		{Name: "Arroz", Quantity: 1, Unit: "kg"},
	})
	assert.Contains(t, msg, "Arroz")
	assert.Contains(t, msg, "Don Pepe")
}

// T-09 / AC-06 (Feature 003) — the work-order quotation WhatsApp
// message lists EVERY item with its line total, plus the customer
// greeting, the business name and the grand total.
func TestWhatsAppService_WorkOrderQuote_ListsAllItems(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.WorkOrderQuote("Carlos", "Carpintería Don Pepe", []WorkOrderQuoteLine{
		{Description: "Madera", Quantity: 2, LineTotal: 40000},
		{Description: "Mano de obra", Quantity: 1, LineTotal: 50000},
	}, 90000)
	assert.Contains(t, msg, "Carlos")
	assert.Contains(t, msg, "Carpintería Don Pepe")
	assert.Contains(t, msg, "Madera")
	assert.Contains(t, msg, "Mano de obra")
	// Line totals and the grand total must be present so the customer
	// sees the breakdown and the price.
	assert.Contains(t, msg, "$40.000")
	assert.Contains(t, msg, "$50.000")
	assert.Contains(t, msg, "$90.000")
}

// An empty customer name is tolerated — the message still goes out
// (Art. I, cero fricción: never block on a missing optional field).
func TestWhatsAppService_WorkOrderQuote_EmptyCustomerName(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.WorkOrderQuote("", "Carpintería Don Pepe", []WorkOrderQuoteLine{
		{Description: "Reparar silla", Quantity: 1, LineTotal: 30000},
	}, 30000)
	assert.Contains(t, msg, "Reparar silla")
	assert.Contains(t, msg, "Carpintería Don Pepe")
	assert.Contains(t, msg, "$30.000")
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

// ── T-07: credit_label_mode in WhatsApp templates (Spec F028 FR-07, AC-04) ─

// TestCreditHandshake_FiarMode verifies the handshake message uses "fiar"
// vocabulary when credit_label_mode is "fiar" (default).
func TestCreditHandshake_FiarMode(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.CreditHandshakeWithMode("Carlos", "Tienda Don Pepe", 10000, "fiar")
	assert.Contains(t, msg, "Carlos")
	assert.Contains(t, msg, "Tienda Don Pepe")
	assert.Contains(t, msg, "$10.000")
	assert.Contains(t, msg, "ha fiado hoy",
		"modo fiar debe usar 'ha fiado hoy' en el handshake")
	assert.NotContains(t, msg, "crédito")
}

// TestCreditHandshake_CreditMode verifies the handshake message uses "crédito"
// vocabulary when credit_label_mode is "credit" (AC-04).
func TestCreditHandshake_CreditMode(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.CreditHandshakeWithMode("Carlos", "Tienda Don Pepe", 10000, "credit")
	assert.Contains(t, msg, "Carlos")
	assert.Contains(t, msg, "Tienda Don Pepe")
	assert.Contains(t, msg, "$10.000")
	assert.Contains(t, msg, "venta a crédito",
		"modo credit debe usar 'venta a crédito' en el handshake")
	assert.NotContains(t, msg, "fiado")
}

// TestCreditReminder_FiarMode verifies the reminder message uses "fiar" vocabulary.
func TestCreditReminder_FiarMode(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.CreditReminderWithMode("Carlos", "Tienda Don Pepe", 15000, "fiar")
	assert.Contains(t, msg, "Carlos")
	assert.Contains(t, msg, "$15.000")
	assert.Contains(t, msg, "fiado",
		"modo fiar debe usar 'fiado' en el recordatorio")
	assert.NotContains(t, msg, "crédito")
}

// TestCreditReminder_CreditMode verifies the reminder message uses "crédito"
// vocabulary when mode is "credit" (AC-04, FR-07).
func TestCreditReminder_CreditMode(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.CreditReminderWithMode("Carlos", "Tienda Don Pepe", 15000, "credit")
	assert.Contains(t, msg, "Carlos")
	assert.Contains(t, msg, "$15.000")
	assert.Contains(t, msg, "venta a crédito",
		"modo credit debe usar 'venta a crédito' en el recordatorio (AC-04)")
	assert.NotContains(t, msg, "fiado")
}

// TestCreditHandshake_LegacyMethodStillUsesFiar verifies backward compatibility:
// the original CreditHandshake (no mode arg) still produces "fiar" text.
func TestCreditHandshake_LegacyMethodStillUsesFiar(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.CreditHandshake("Carlos", "Tienda Don Pepe", 10000)
	assert.Contains(t, msg, "fiado", "el método legacy debe seguir usando 'fiado' (AC-06)")
}

// TestCreditReminder_LegacyMethodStillWorks verifies backward compatibility:
// the original CreditReminder (no mode arg) still uses "fiar" vocabulary (AC-06).
func TestCreditReminder_LegacyMethodStillWorks(t *testing.T) {
	svc := NewWhatsAppService()
	msg := svc.CreditReminder("Carlos", "Tienda Don Pepe", 15000)
	assert.Contains(t, msg, "Carlos")
	assert.Contains(t, msg, "$15.000")
	assert.Contains(t, msg, "fiado", "el método legacy debe usar 'fiado' (AC-06, retrocompat)")
}
