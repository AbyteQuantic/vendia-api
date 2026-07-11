// Spec: specs/104-moderacion-f1-lexico/spec.md
package moderation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestEvaluateProduct_TablaDeCasos cubre AC-03/AC-04: veredictos por
// categoría, matching sin tildes/mayúsculas, palabra completa y excepciones.
func TestEvaluateProduct_TablaDeCasos(t *testing.T) {
	cases := []struct {
		name       string
		product    string
		category   string
		wantStatus string
		wantCat    string
	}{
		// allowed — el inventario normal de una tienda jamás se toca
		{"arroz normal", "Arroz Diana 500g", "granos", StatusAllowed, ""},
		{"cerveza es allowed (18+ va por is_age_restricted)", "Cerveza Águila 330ml", "bebidas", StatusAllowed, ""},
		{"palabra completa: chancleta no es chance", "Chancletas talla 40", "calzado", StatusAllowed, ""},
		{"excepción: pistola de agua", "Pistola de agua grande", "juguetes", StatusAllowed, ""},
		{"excepción: pistola de silicona", "Pistola de silicona 20W", "ferreteria", StatusAllowed, ""},

		// blocked
		{"pólvora con tilde", "Volador PÓLVORA x12", "temporada", StatusBlocked, "polvora"},
		{"pólvora sin tilde", "polvora surtida", "", StatusBlocked, "polvora"},
		{"arma real", "Revólver calibre 38", "", StatusBlocked, "armas"},
		{"medicamento de receta", "Tramadol gotas 50ml", "farmacia", StatusBlocked, "medicamentos_receta"},
		{"tabaco (Ley 1335: sin publicidad)", "Marlboro rojo x20", "cigarrillos", StatusBlocked, "tabaco"},
		{"vapeador (Ley 2354)", "Vaper desechable 5000 puffs", "", StatusBlocked, "tabaco"},
		{"chance (Ley 643)", "Chance del día", "", StatusBlocked, "apuestas"},
		{"gota a gota", "Préstamos gota a gota fácil", "servicios", StatusBlocked, "financiero_ilegal"},
		{"fauna multi-palabra", "Tortuga hicotea viva", "", StatusBlocked, "fauna"},
		{"categoría también evalúa", "Rubio suave", "cigarrillos", StatusBlocked, "tabaco"},

		// review
		{"OTC a revisión", "Acetaminofén 500mg x10", "farmacia", StatusReview, "medicamentos"},
		{"réplica a revisión", "Perfume réplica 1.1", "", StatusReview, "replicas"},

		// precedencia: blocked gana sobre review
		{"blocked > review", "Acetaminofén y tramadol", "", StatusBlocked, "medicamentos_receta"},

		// vacío
		{"texto vacío", "", "", StatusAllowed, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := EvaluateProduct(tc.product, tc.category, "")
			assert.Equal(t, tc.wantStatus, v.Status, "status de %q", tc.product)
			assert.Equal(t, tc.wantCat, v.Category, "categoría de %q", tc.product)
		})
	}
}

// TestEvaluateText_DescripcionTambienCuenta — el hit puede venir de la
// descripción, no solo del nombre.
func TestEvaluateText_DescripcionTambienCuenta(t *testing.T) {
	v := EvaluateProduct("Combo especial", "", "incluye cigarrillos y encendedor")
	assert.Equal(t, StatusBlocked, v.Status)
	assert.Equal(t, "tabaco", v.Category)
}
