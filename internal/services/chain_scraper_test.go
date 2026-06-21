// Spec: specs/077-compra-inteligente-insumos/spec.md
package services

import "testing"

func TestParsePackaging_NormalizesToBaseUnit(t *testing.T) {
	cases := []struct {
		name     string
		price    float64
		wantUnit string
		wantQty  float64
		wantPer  float64
	}{
		{"Aceite Canola 1 Lt", 21742, "ml", 1000, 21.742},
		{"ACEITE CANOLA 900ML", 9990, "ml", 900, 11.1},
		{"Aceite Oliva 2 L", 40000, "ml", 2000, 20},
		{"Arroz Diana 500 g", 2500, "g", 500, 5},
		{"Panela 1 kg", 6000, "g", 1000, 6},
		{"Sin tamaño conocido", 5000, "", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, q, per := parsePackaging(c.name, c.price)
			if u != c.wantUnit || q != c.wantQty {
				t.Fatalf("unit/qty = %q/%v, want %q/%v", u, q, c.wantUnit, c.wantQty)
			}
			if c.wantUnit != "" && (per < c.wantPer-0.01 || per > c.wantPer+0.01) {
				t.Errorf("pricePerBase = %v, want ~%v", per, c.wantPer)
			}
		})
	}
}

func TestIsFoodProduct_RejectsCosmetics(t *testing.T) {
	if isFoodProduct("Aceite Anti Estrias Piel De Oro 160 Ml", "Aceites") {
		t.Error("anti-estrías NO debe ser comestible")
	}
	if isFoodProduct("ACEITE CORPORAL ALMENDRAS 500 ML", "Aceites") {
		t.Error("corporal NO debe ser comestible")
	}
	if isFoodProduct("4 Botellas Dispensadoras Aceite", "Consolas y videojuegos") {
		t.Error("dispensador/consola NO debe ser comestible")
	}
	if !isFoodProduct("ACEITE CANOLA MEDALLA DE ORO 900ML", "Aceites") {
		t.Error("aceite de canola SÍ debe ser comestible")
	}
}
