// Spec: specs/077-compra-inteligente-insumos/spec.md
//
// Auditoría 2026-07-03 del cron semanal "scrape-chains" (evalúa precios de
// cadenas en las ciudades donde hay tenants activos, Spec 077 F4). Este
// archivo no existía — el handler nunca había tenido un test de flujo
// completo (solo las funciones puras de internal/services/chain_scraper_test.go
// y chain_price_query_test.go). Cubre:
//   - el gate CRON_TOKEN (mismo patrón que los demás jobs internos)
//   - el fix de la auditoría: si el scraping no trae NADA (las 2 cadenas
//     fallan/cambian de contrato), el handler ahora responde 502 en vez de
//     200 silencioso — así el step de cron-jobs.yml falla y GitHub avisa.
//   - el camino feliz: inserta filas reales en chain_price y responde 200.
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupScrapeChainsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupQuoteDB(t)
	require.NoError(t, db.AutoMigrate(&models.ChainPrice{}))
	return db
}

func mountScrapeChains(db *gorm.DB, sources []services.ChainSource) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/internal/jobs/scrape-chains", handlers.ScrapeChainsJob(db, sources))
	return r
}

func callScrapeChains(r *gin.Engine, token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/internal/jobs/scrape-chains", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r.ServeHTTP(w, req)
	return w
}

func TestScrapeChainsJob_AuthGate(t *testing.T) {
	db := setupScrapeChainsDB(t)
	r := mountScrapeChains(db, nil)

	t.Setenv("CRON_TOKEN", "")
	assert.Equal(t, http.StatusServiceUnavailable, callScrapeChains(r, "x").Code,
		"sin CRON_TOKEN → 503 fail-closed")

	t.Setenv("CRON_TOKEN", "tok")
	assert.Equal(t, http.StatusUnauthorized, callScrapeChains(r, "wrong").Code,
		"token incorrecto → 401")
}

// TestScrapeChainsJob_BothChainsFail_Returns502 cubre el fix de la
// auditoría: antes, si las 2 cadenas fallaban (API caída, bloqueo,
// cambio de contrato), el handler igual respondía 200 con scraped=0 —
// el cron "corría bien" cada semana sin que nadie notara que llevaba
// meses sin traer datos nuevos. Ahora responde 502.
func TestScrapeChainsJob_BothChainsFail_Returns502(t *testing.T) {
	db := setupScrapeChainsDB(t)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	sources := []services.ChainSource{{Chain: "exito", BaseURL: broken.URL}}
	r := mountScrapeChains(db, sources)
	t.Setenv("CRON_TOKEN", "tok")

	w := callScrapeChains(r, "tok")
	assert.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.EqualValues(t, 0, body["scraped"])

	var count int64
	db.Model(&models.ChainPrice{}).Count(&count)
	assert.Zero(t, count, "no debe quedar ninguna fila insertada")
}

// TestScrapeChainsJob_HappyPath_InsertsRowsAndReturns200 simula una cadena
// que SÍ responde con catálogo VTEX válido — el job debe insertar filas
// reales en chain_price (scoped a la ciudad del tenant) y responder 200.
func TestScrapeChainsJob_HappyPath_InsertsRowsAndReturns200(t *testing.T) {
	db := setupScrapeChainsDB(t)
	require.NoError(t, db.Create(&models.Tenant{
		BaseModel: models.BaseModel{ID: "tenant-scrape-1"},
		City:      "Fusagasugá", Latitude: 4.34, Longitude: -74.36,
	}).Error)

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{
			"productName": "Arroz Diana 500 g",
			"brand": "Diana",
			"linkText": "arroz-diana-500g",
			"categories": ["/Mercado/Arroz/"],
			"items": [{"itemId": "sku-1", "ean": "123", "sellers": [
				{"commertialOffer": {"Price": 2500, "ListPrice": 2800}}
			]}]
		}]`))
	}))
	defer fake.Close()

	sources := []services.ChainSource{{Chain: "exito", BaseURL: fake.URL}}
	r := mountScrapeChains(db, sources)
	t.Setenv("CRON_TOKEN", "tok")

	w := callScrapeChains(r, "tok")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	scraped, _ := body["scraped"].(float64)
	assert.Greater(t, scraped, float64(0))
	assert.Equal(t, []any{"Fusagasugá"}, body["cities"])

	var rows []models.ChainPrice
	require.NoError(t, db.Find(&rows).Error)
	require.NotEmpty(t, rows)
	assert.Equal(t, "Fusagasugá", rows[0].City)
	assert.Equal(t, 2500.0, rows[0].Price)
}
