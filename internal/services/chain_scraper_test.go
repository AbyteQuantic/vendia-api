// Spec: specs/077-compra-inteligente-insumos/spec.md
package services

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

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

// Auditoría 2026-07-03: term se interpolaba crudo en la URL (fmt.Sprintf sin
// url.QueryEscape). Hoy es inofensivo porque ScrapeTerms son 22 palabras fijas
// sin caracteres especiales, pero FetchVTEXProducts es exportada y reutilizable
// — un término con espacio/'&'/'#' rompería la URL o inyectaría parámetros
// extra a VTEX si esta función se reutiliza con input menos controlado.
func TestFetchVTEXProducts_EscapesTermInURL(t *testing.T) {
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	client := srv.Client()
	dangerousTerm := "aceite de oliva & vinagre#1"
	_, err := FetchVTEXProducts(client, srv.URL, dangerousTerm)
	if err != nil {
		t.Fatalf("FetchVTEXProducts error: %v", err)
	}

	// El servidor debe recibir 'ft' como UN SOLO valor con el término
	// completo — no debe interpretarse '&'/'#' como separadores de query
	// params o de fragment, lo que pasaría si no se escapa.
	values, err := url.ParseQuery(gotRawQuery)
	if err != nil {
		t.Fatalf("no se pudo parsear la query recibida %q: %v", gotRawQuery, err)
	}
	if got := values.Get("ft"); got != dangerousTerm {
		t.Fatalf("el término llegó alterado al servidor: got %q, want %q",
			got, dangerousTerm)
	}
}

// Auditoría 2026-07-03: la pausa de cortesía (400ms) vivía DESPUÉS del bloque
// de inserción, así que un `continue` por error/respuesta vacía la saltaba
// por completo — justo el escenario (la cadena empezando a fallar/bloquear)
// en el que la pausa más importa. Este test usa un servidor que SIEMPRE
// falla (500) para los 22 términos de ScrapeTerms y verifica que, aun así,
// el tiempo total sea consistente con una pausa entre cada intento — si el
// bug reaparece, el loop corre casi instantáneo (sin dormir nada).
func TestScrapeChainsForCity_SleepsBetweenAttempts_EvenOnFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.ChainPrice{}))

	var hits int64
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	start := time.Now()
	inserted := ScrapeChainsForCity(db, "Fusagasugá",
		[]ChainSource{{Chain: "exito", BaseURL: broken.URL}})
	elapsed := time.Since(start)

	if inserted != 0 {
		t.Fatalf("no debe insertar nada si todas las respuestas fallan, insertó %d", inserted)
	}
	if got := atomic.LoadInt64(&hits); got != int64(len(ScrapeTerms)) {
		t.Fatalf("debe intentar los %d términos aunque todos fallen, intentó %d",
			len(ScrapeTerms), got)
	}
	// (len(ScrapeTerms)-1) pausas de 400ms — margen amplio (250ms) para no ser
	// flaky en CI, pero suficiente para detectar el bug (0 pausas ≈ <1s total).
	minExpected := time.Duration(len(ScrapeTerms)-1) * 250 * time.Millisecond
	if elapsed < minExpected {
		t.Fatalf("el loop corrió en %v, muy rápido para %d intentos con pausa "+
			"de cortesía entre cada uno (¿el continue por error salta el sleep?) — "+
			"mínimo esperado %v", elapsed, len(ScrapeTerms), minExpected)
	}
}
