package services

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type WhatsAppService struct{}

func NewWhatsAppService() *WhatsAppService {
	return &WhatsAppService{}
}

func (s *WhatsAppService) ReceiptMessage(businessName string, total float64, receiptURL string) string {
	return fmt.Sprintf(
		"¡Hola! Gracias por comprar en %s. Su total fue $%s. "+
			"Vea su recibo aquí: %s. ¡Guarde este número!",
		businessName, formatCOP(total), receiptURL)
}

func (s *WhatsAppService) CreditHandshake(customerName, businessName string, amount float64) string {
	return fmt.Sprintf(
		"Hola %s. %s le ha fiado hoy $%s. "+
			"Para confirmar, responda 'Sí' a este mensaje.",
		customerName, businessName, formatCOP(amount))
}

func (s *WhatsAppService) CreditReminder(customerName, businessName string, balance float64) string {
	return fmt.Sprintf(
		"Hola %s. Le recuerdo que tiene un saldo pendiente de "+
			"$%s en %s. ¡Gracias!",
		customerName, formatCOP(balance), businessName)
}

func (s *WhatsAppService) SupplierOrder(contactName, productName string, quantity int, ownerName string) string {
	return fmt.Sprintf(
		"Hola %s, por favor en mi pedido de mañana me incluyes "+
			"%d unidades de %s. Gracias, %s.",
		contactName, quantity, productName, ownerName)
}

// PurchaseOrderLine is one item rendered into a purchase-order
// WhatsApp message — the insumo/producto name, how much, and its unit.
type PurchaseOrderLine struct {
	Name     string
	Quantity float64
	Unit     string
}

// PurchaseOrder builds the WhatsApp message a tendero sends to a
// proveedor with the COMPLETE list of items of a purchase order
// (Feature 002 FR-04, AC-02). Each line is "- <qty> <unit> de <name>".
func (s *WhatsAppService) PurchaseOrder(contactName, ownerName string, lines []PurchaseOrderLine) string {
	greeting := "Hola"
	if strings.TrimSpace(contactName) != "" {
		greeting = "Hola " + contactName
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s, le hago el siguiente pedido:\n", greeting)
	for _, line := range lines {
		fmt.Fprintf(&b, "- %s %s de %s\n",
			formatQuantity(line.Quantity), line.Unit, line.Name)
	}
	fmt.Fprintf(&b, "Gracias, %s.", ownerName)
	return b.String()
}

// formatQuantity renders a quantity without a trailing ".0" for whole
// numbers (24 not 24.0) but keeps real decimals (2.5 kg).
func formatQuantity(q float64) string {
	return strconv.FormatFloat(q, 'f', -1, 64)
}

func (s *WhatsAppService) BuildURL(phone, message string) string {
	phone = strings.ReplaceAll(phone, " ", "")
	phone = strings.ReplaceAll(phone, "+", "")
	phone = strings.TrimPrefix(phone, "57")
	if len(phone) == 10 {
		phone = "57" + phone
	}
	return fmt.Sprintf("https://wa.me/%s?text=%s", phone, url.QueryEscape(message))
}

func formatCOP(amount float64) string {
	intAmount := int64(amount)
	s := fmt.Sprintf("%d", intAmount)
	if len(s) <= 3 {
		return s
	}
	var result []string
	for i := len(s); i > 0; i -= 3 {
		start := i - 3
		if start < 0 {
			start = 0
		}
		result = append([]string{s[start:i]}, result...)
	}
	return strings.Join(result, ".")
}
