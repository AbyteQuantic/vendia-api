// Spec: specs/077-compra-inteligente-insumos/spec.md
package services

import "math"

// PackagingCost — resultado del cálculo de compra por EMPAQUE completo.
type PackagingCost struct {
	Packs          int     // empaques a comprar (entero ≥ 1 cuando se conoce el empaque)
	Cost           float64 // costo real = Packs × precioEmpaque (NUNCA menor a un empaque)
	Purchased      float64 // cantidad total comprada = Packs × PackQty (unidad base)
	Leftover       float64 // sobrante reservado = Purchased − faltante (≥ 0)
	PackagingKnown bool    // false → no se conoce el empaque; usar fallback per-unidad
}

// ComputePackagingCost calcula cuántos EMPAQUES completos cubren el faltante y
// cuánto producto queda reservado (Spec 077). Nadie vende media bolsa: si el
// proveedor vende el empaque entero, el costo nunca puede ser menor a un empaque.
//
//   - shortfall: cuánto falta (necesario − stock), en la unidad base del insumo.
//   - packQty: tamaño del empaque en la MISMA unidad base (ej. 1000 para 1 L).
//   - packagePrice: precio del empaque completo.
//
// Si packQty <= 0 (no se conoce el empaque), devuelve PackagingKnown=false y el
// handler debe caer al cálculo per-unidad marcándolo "aproximado".
//
// Ejemplo: ComputePackagingCost(2, 1000, 4200) ⇒ Packs=1, Cost=4200,
// Purchased=1000, Leftover=998, PackagingKnown=true.
func ComputePackagingCost(shortfall, packQty, packagePrice float64) PackagingCost {
	if packQty <= 0 || packagePrice <= 0 {
		return PackagingCost{PackagingKnown: false}
	}
	if shortfall <= 0 {
		return PackagingCost{PackagingKnown: true}
	}
	packs := int(math.Ceil(shortfall / packQty))
	if packs < 1 {
		packs = 1 // nadie compra menos de un empaque
	}
	purchased := float64(packs) * packQty
	cost := math.Round(float64(packs)*packagePrice*100) / 100
	leftover := math.Round((purchased-shortfall)*100) / 100
	if leftover < 0 {
		leftover = 0
	}
	return PackagingCost{
		Packs:          packs,
		Cost:           cost,
		Purchased:      purchased,
		Leftover:       leftover,
		PackagingKnown: true,
	}
}
