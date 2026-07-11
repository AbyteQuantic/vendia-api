// Spec: specs/104-moderacion-f1-lexico/spec.md
//
// Léxico determinístico de moderación — la capa que NUNCA falla (sin red, sin
// IA, CPU puro). Clasifica un producto en allowed | review | blocked según
// patrones normalizados con base legal colombiana. `blocked` y `review` solo
// excluyen al producto de las superficies PÚBLICAS (catálogo en línea,
// difusión); el POS presencial del tendero jamás se bloquea.
//
// Este paquete no importa nada de internal/ (lo consumen models, services y
// handlers) — solo stdlib y x/text.
package moderation

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Estados de moderación de un producto.
const (
	StatusAllowed = "allowed"
	StatusReview  = "review"
	StatusBlocked = "blocked"
)

// Verdict — resultado de evaluar un texto contra el léxico.
type Verdict struct {
	// Status: allowed | review | blocked.
	Status string
	// Category: la categoría del hit (armas, tabaco, polvora…). Vacía si allowed.
	Category string
}

// rule — un grupo de patrones de una categoría legal. Los patterns se
// comparan NORMALIZADOS (sin tildes, minúsculas) y por palabra completa.
// Las exceptions desactivan el hit (anti falso positivo: "pistola de agua").
type rule struct {
	category   string
	status     string
	patterns   []string
	exceptions []string
}

// lexicon — base legal por categoría (detalle y fuentes en
// legal/SISTEMA_MODERACION_IA_PROPUESTA.md §1).
var lexicon = []rule{
	{
		// Decreto Ley 2535/1993; CP art. 365.
		category: "armas",
		status:   StatusBlocked,
		patterns: []string{
			"revolver", "pistola", "escopeta", "fusil", "subametralladora",
			"municion", "municiones", "cartucho calibre", "cartuchos calibre",
			"granada", "silenciador para arma", "arma traumatica", "changon",
		},
		exceptions: []string{
			"de agua", "de juguete", "de silicona", "de pintura", "de calor",
			"de aire caliente", "de burbujas", "juguete",
		},
	},
	{
		// Ley 30/1986; CP art. 376.
		category: "drogas",
		status:   StatusBlocked,
		patterns: []string{
			"marihuana", "cocaina", "bazuco", "tusi", "extasis", "popper",
			"poppers", "lsd", "cripa", "ketamina", "2cb",
		},
	},
	{
		// Decreto 677/1995 (INVIMA): receta → bloqueado; OTC → revisión
		// (una tienda de barrio no es droguería con dirección técnica).
		category: "medicamentos_receta",
		status:   StatusBlocked,
		patterns: []string{
			"tramadol", "diazepam", "clonazepam", "alprazolam", "morfina",
			"misoprostol", "amoxicilina", "azitromicina", "antibiotico",
			"antibioticos", "sildenafil", "viagra", "cialis", "ozempic",
			"rivotril",
		},
	},
	{
		category: "medicamentos",
		status:   StatusReview,
		patterns: []string{
			"acetaminofen", "ibuprofeno", "aspirina", "dolex", "advil",
			"noxpirin", "loratadina", "omeprazol", "buscapina", "naproxeno",
			"desparasitante", "antigripal",
		},
	},
	{
		// Ley 670/2001; Decreto 4481/2006.
		category: "polvora",
		status:   StatusBlocked,
		patterns: []string{
			"polvora", "volador", "voladores", "totes", "pirotecnico",
			"pirotecnicos", "fuegos artificiales", "papeleta explosiva",
			"luces de bengala", "bengalas",
		},
	},
	{
		// Ley 611/2000; CP art. 328A. Solo patrones inequívocos (multi-palabra)
		// para no bloquear "galletas tortuga" ni productos con animales en la marca.
		category: "fauna",
		status:   StatusBlocked,
		patterns: []string{
			"loro real", "guacamaya", "tortuga hicotea", "hicotea", "icotea",
			"carne de monte", "mono titi", "fauna silvestre", "piel de babilla",
			"tortuga morrocoy", "morrocoy",
		},
	},
	{
		// Ley 643/2001 (monopolio rentístico, Coljuegos).
		category: "apuestas",
		status:   StatusBlocked,
		patterns: []string{"chance", "rifa", "rifas", "apuestas", "casino en linea"},
	},
	{
		// CP art. 316 (captación ilegal); Decreto 4336/2008.
		category: "financiero_ilegal",
		status:   StatusBlocked,
		patterns: []string{
			"gota a gota", "prestamos gota", "inversion garantizada",
			"rendimiento garantizado", "captacion de dinero", "paga diario",
		},
	},
	{
		// CP arts. 213 ss.; política de plataforma.
		category: "sexual",
		status:   StatusBlocked,
		patterns: []string{
			"servicios sexuales", "masajes eroticos", "contenido para adultos",
			"escort", "prepago",
		},
	},
	{
		// Ley 1335/2009 arts. 14-16 + Ley 2354/2024: la VENTA en mostrador es
		// legal; TODA publicidad (catálogo público, difusión) está prohibida.
		// blocked = solo fuera de superficies públicas; el POS sigue intacto.
		category: "tabaco",
		status:   StatusBlocked,
		patterns: []string{
			"cigarrillo", "cigarrillos", "tabaco", "vaper", "vape", "vapeador",
			"vapeadores", "pod desechable", "nicotina", "iqos", "marlboro",
			"lucky strike", "rothmans", "chesterfield",
		},
	},
	{
		// CP art. 447 (receptación); Decreto 1630/2011 (IMEI). Solo señales explícitas.
		category: "robados",
		status:   StatusBlocked,
		patterns: []string{
			"sin papeles", "imei bloqueado", "reportado", "sin factura ni caja",
		},
	},
	{
		// Falsedad marcaria — señal débil, a revisión (réplicas de perfume/ropa
		// son comunes y a veces se anuncian como tales).
		category: "replicas",
		status:   StatusReview,
		patterns: []string{"replica", "replicas", "imitacion", "tipo original"},
	},
}

// normalize — sin tildes, minúsculas, espacios colapsados. Copia mínima de
// services.NormalizeText (este paquete no puede importar services: ciclo).
func normalize(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, _ := transform.String(t, s)
	out = strings.ToLower(out)
	return strings.Join(strings.Fields(out), " ")
}

// containsWord — true si pattern aparece en text como palabra(s) completa(s):
// "chance" matchea "chance del dia" pero no "chancleta". Ambos ya normalizados.
func containsWord(text, pattern string) bool {
	padded := " " + text + " "
	return strings.Contains(padded, " "+pattern+" ")
}

// EvaluateText clasifica un texto libre (nombre + categoría + descripción de
// producto, o el copy de una promo). Precedencia: blocked > review > allowed.
func EvaluateText(parts ...string) Verdict {
	text := normalize(strings.Join(parts, " "))
	if text == "" {
		return Verdict{Status: StatusAllowed}
	}

	best := Verdict{Status: StatusAllowed}
	for _, r := range lexicon {
		hit := false
		for _, p := range r.patterns {
			if containsWord(text, p) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		excepted := false
		for _, e := range r.exceptions {
			if strings.Contains(text, e) {
				excepted = true
				break
			}
		}
		if excepted {
			continue
		}
		if r.status == StatusBlocked {
			return Verdict{Status: StatusBlocked, Category: r.category}
		}
		if best.Status == StatusAllowed {
			best = Verdict{Status: StatusReview, Category: r.category}
		}
	}
	return best
}

// EvaluateProduct — atajo para las tres señales de texto de un producto.
func EvaluateProduct(name, category, description string) Verdict {
	return EvaluateText(name, category, description)
}
