// Spec: specs/077-compra-inteligente-insumos/spec.md
package services

import "strings"

// ToBaseQty convierte una cantidad a su unidad BASE canónica para poder COMPARAR
// y CALCULAR sin importar si el insumo viene en kg y el empaque en g: masa→g,
// volumen→ml, conteo→und. Devuelve (cantidadEnBase, dimensión). Así un insumo en
// kg y un empaque de cadena en g quedan en la misma base (gramos) y el costo por
// empaque entero cuadra (Spec 077 — fix del "sin sentido").
func ToBaseQty(qty float64, unit string) (float64, string) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "mg":
		return qty / 1000, "mass"
	case "g", "gr", "gramo", "gramos":
		return qty, "mass"
	case "kg", "kgs", "kilo", "kilos", "kilogramo", "kilogramos":
		return qty * 1000, "mass"
	case "ml", "cc", "mililitro", "mililitros":
		return qty, "vol"
	case "l", "lt", "lts", "litro", "litros":
		return qty * 1000, "vol"
	case "unidad", "unidades", "und", "unid", "u", "":
		return qty, "count"
	default:
		return qty, "other"
	}
}
