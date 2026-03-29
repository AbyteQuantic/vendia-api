package services

import (
	"fmt"
	"net/url"
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
