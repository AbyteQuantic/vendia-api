// Spec: specs/098-aporte-automatico-catalogo/spec.md — Fase 2.
package services

import "strings"

// ValidRetailBarcode — true si es un código de barras de RETAIL válido
// (EAN-8, UPC-A/12, EAN-13, ITF-14) con dígito de control GTIN mod-10 correcto.
// Estricto a propósito: un SKU interno o número arbitrario NO califica para el
// catálogo COMPARTIDO (Spec 098 Fase 2). Portado de barcode_validator.dart.
func ValidRetailBarcode(code string) bool {
	code = strings.TrimSpace(code)
	n := len(code)
	if n != 8 && n != 12 && n != 13 && n != 14 {
		return false
	}
	sum := 0
	for i := 0; i < n; i++ {
		r := code[i]
		if r < '0' || r > '9' {
			return false
		}
		d := int(r - '0')
		weight := 1
		if (n-1-i)%2 != 0 {
			weight = 3
		}
		sum += d * weight
	}
	return sum%10 == 0
}
