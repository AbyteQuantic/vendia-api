// Spec: specs/028-copy-fiar-credito-configurable/spec.md
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
	// receiptURL vacío = sin link en el mensaje (el portal público de
	// recibos no existe aún; antes se enviaba un enlace roto a vendia.co).
	if receiptURL == "" {
		return fmt.Sprintf(
			"¡Hola! Gracias por comprar en %s. Su total fue $%s. "+
				"¡Guarde este número!",
			businessName, formatCOP(total))
	}
	return fmt.Sprintf(
		"¡Hola! Gracias por comprar en %s. Su total fue $%s. "+
			"Vea su recibo aquí: %s. ¡Guarde este número!",
		businessName, formatCOP(total), receiptURL)
}

func (s *WhatsAppService) CreditHandshake(customerName, businessName string, amount float64) string {
	return s.CreditHandshakeWithMode(customerName, businessName, amount, "fiar")
}

// CreditHandshakeWithMode builds the credit-opening confirmation message.
// The verb phrase adapts to the tenant's credit_label_mode (Spec F028 FR-07).
// mode: "fiar" (default) or "credit".
func (s *WhatsAppService) CreditHandshakeWithMode(customerName, businessName string, amount float64, mode string) string {
	labels := GetCreditLabels(mode)
	return fmt.Sprintf(
		"Hola %s. %s %s $%s. "+
			"Para confirmar, responda 'Sí' a este mensaje.",
		customerName, businessName, labels.WhatsAppHandshakeVerb, formatCOP(amount))
}

func (s *WhatsAppService) CreditReminder(customerName, businessName string, balance float64) string {
	return s.CreditReminderWithMode(customerName, businessName, balance, "fiar")
}

// CreditReminderWithMode builds the credit-reminder message.
// The noun phrase adapts to the tenant's credit_label_mode (Spec F028 FR-07, AC-04).
// mode: "fiar" (default) or "credit".
func (s *WhatsAppService) CreditReminderWithMode(customerName, businessName string, balance float64, mode string) string {
	labels := GetCreditLabels(mode)
	return fmt.Sprintf(
		"Hola %s. Le recordamos que tiene un %s de $%s en %s. ¡Gracias!",
		customerName, labels.WhatsAppReminderDebt, formatCOP(balance), businessName)
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

// WorkOrderQuoteLine is one item rendered into a work-order quotation
// WhatsApp message — its description, how much, and its line total.
type WorkOrderQuoteLine struct {
	Description string
	Quantity    float64
	LineTotal   float64
}

// WorkOrderQuote builds the WhatsApp message a taller owner sends to a
// customer with the COMPLETE breakdown of a work-order quotation
// (Feature 003 FR-06, AC-06). Each line is
// "- <qty>x <description>: $<line total>".
func (s *WhatsAppService) WorkOrderQuote(customerName, businessName string, lines []WorkOrderQuoteLine, total float64) string {
	greeting := "Hola"
	if strings.TrimSpace(customerName) != "" {
		greeting = "Hola " + customerName
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s, esta es la cotización de %s:\n", greeting, businessName)
	for _, line := range lines {
		fmt.Fprintf(&b, "- %sx %s: $%s\n",
			formatQuantity(line.Quantity), line.Description, formatCOP(line.LineTotal))
	}
	fmt.Fprintf(&b, "Total: $%s", formatCOP(total))
	return b.String()
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
