// Spec: specs/073-activacion-tiendas-gtm/gtm.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupActivationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Tenant{},
		&models.Product{},
		&models.Sale{},
		&models.CreditAccount{},
	))
	return db
}

func mountActivationRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/admin/activation", handlers.AdminActivationFunnel(db))
	return r
}

// TestAdminActivation_EmptyDB — sin tenants, el embudo es todo ceros y la
// lista de tiendas viene vacía pero presente (no nil).
func TestAdminActivation_EmptyDB(t *testing.T) {
	db := setupActivationDB(t)
	r := mountActivationRouter(db)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/activation", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	funnel := resp["funnel"].(map[string]any)
	assert.EqualValues(t, 0, funnel["registradas"])
	assert.EqualValues(t, 0, funnel["onboardeadas"])
	assert.EqualValues(t, 0, funnel["activas_7d"])
	assert.EqualValues(t, 0, funnel["activas_28d"])
	assert.EqualValues(t, 0, funnel["pagas"])
	assert.NotNil(t, resp["tiendas"])
}

// TestAdminActivation_Funnel reproduce el embudo del GTM 073 con datos
// sintéticos que cubren cada etapa y cada fuga.
func TestAdminActivation_Funnel(t *testing.T) {
	db := setupActivationDB(t)
	now := time.Now().UTC()

	// A — Activa real: productos + venta reciente + fiado reciente, paga.
	db.Create(&models.Tenant{
		BaseModel:          models.BaseModel{ID: "aaaaaaaa-0000-0000-0000-000000000001", CreatedAt: now.Add(-80 * 24 * time.Hour)},
		BusinessName:       "Don Brayan", SubscriptionStatus: "active", Phone: "3000000001",
	})
	db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p1"}, TenantID: "aaaaaaaa-0000-0000-0000-000000000001"})
	db.Create(&models.Sale{BaseModel: models.BaseModel{ID: "s1", CreatedAt: now.Add(-2 * 24 * time.Hour)}, TenantID: "aaaaaaaa-0000-0000-0000-000000000001"})
	db.Create(&models.CreditAccount{BaseModel: models.BaseModel{ID: "c1", CreatedAt: now.Add(-2 * 24 * time.Hour)}, TenantID: "aaaaaaaa-0000-0000-0000-000000000001"})

	// B — Onboardeada pero NO activa: cargó productos, 0 ventas (la fuga cara).
	db.Create(&models.Tenant{
		BaseModel:          models.BaseModel{ID: "bbbbbbbb-0000-0000-0000-000000000002", CreatedAt: now.Add(-5 * 24 * time.Hour)},
		BusinessName:       "Melons & Cherries", SubscriptionStatus: "trial", Phone: "3000000002",
	})
	db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p2"}, TenantID: "bbbbbbbb-0000-0000-0000-000000000002"})
	db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p3"}, TenantID: "bbbbbbbb-0000-0000-0000-000000000002"})

	// C — Activa solo 28d (venta vieja > 7d): churn temprano.
	db.Create(&models.Tenant{
		BaseModel:          models.BaseModel{ID: "cccccccc-0000-0000-0000-000000000003", CreatedAt: now.Add(-30 * 24 * time.Hour)},
		BusinessName:       "pamy", SubscriptionStatus: "trial", Phone: "3000000003",
	})
	db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p4"}, TenantID: "cccccccc-0000-0000-0000-000000000003"})
	db.Create(&models.Sale{BaseModel: models.BaseModel{ID: "s2", CreatedAt: now.Add(-20 * 24 * time.Hour)}, TenantID: "cccccccc-0000-0000-0000-000000000003"})

	// D — Registrada, nunca montó: sin productos.
	db.Create(&models.Tenant{
		BaseModel:          models.BaseModel{ID: "dddddddd-0000-0000-0000-000000000004", CreatedAt: now.Add(-7 * 24 * time.Hour)},
		BusinessName:       "don pedro", SubscriptionStatus: "trial", Phone: "3000000004",
	})

	// SEED — datos de prueba (074), prefijo 5eed: NO debe contar.
	db.Create(&models.Tenant{
		BaseModel:          models.BaseModel{ID: "5eed0000-0000-0000-0000-000000000099", CreatedAt: now},
		BusinessName:       "Semilla proveedor", SubscriptionStatus: "trial", Phone: "3000000099",
	})
	db.Create(&models.Product{BaseModel: models.BaseModel{ID: "pseed"}, TenantID: "5eed0000-0000-0000-0000-000000000099"})

	r := mountActivationRouter(db)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/activation", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Funnel struct {
			Registradas  int64 `json:"registradas"`
			Onboardeadas int64 `json:"onboardeadas"`
			Activas7d    int64 `json:"activas_7d"`
			Activas28d   int64 `json:"activas_28d"`
			Pagas        int64 `json:"pagas"`
		} `json:"funnel"`
		Tiendas []struct {
			Tienda            string `json:"tienda"`
			Productos         int64  `json:"productos"`
			VentasTotal       int64  `json:"ventas_total"`
			UltimaVenta       string `json:"ultima_venta"`
			Fiados            int64  `json:"fiados"`
			Plan              string `json:"plan"`
			DiasDesdeRegistro int    `json:"dias_desde_registro"`
		} `json:"tiendas"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Embudo: 4 reales (el seed 5eed queda fuera).
	assert.EqualValues(t, 4, resp.Funnel.Registradas, "seed 5eed no cuenta")
	assert.EqualValues(t, 3, resp.Funnel.Onboardeadas, "A,B,C tienen productos")
	assert.EqualValues(t, 1, resp.Funnel.Activas7d, "solo A vendió en 7d")
	assert.EqualValues(t, 2, resp.Funnel.Activas28d, "A (venta+fiado) y C (venta vieja)")
	assert.EqualValues(t, 1, resp.Funnel.Pagas, "solo A active")

	// Desglose: 4 tiendas, ordenado por ventas desc → Don Brayan primero.
	require.Len(t, resp.Tiendas, 4)
	assert.Equal(t, "Don Brayan", resp.Tiendas[0].Tienda)
	assert.EqualValues(t, 1, resp.Tiendas[0].VentasTotal)
	assert.EqualValues(t, 1, resp.Tiendas[0].Fiados)
	assert.NotEmpty(t, resp.Tiendas[0].UltimaVenta)
	assert.Equal(t, "active", resp.Tiendas[0].Plan)
	assert.GreaterOrEqual(t, resp.Tiendas[0].DiasDesdeRegistro, 79)

	// Melons: 2 productos, 0 ventas, sin última venta — la fila a llamar.
	var melons *struct {
		Tienda            string `json:"tienda"`
		Productos         int64  `json:"productos"`
		VentasTotal       int64  `json:"ventas_total"`
		UltimaVenta       string `json:"ultima_venta"`
		Fiados            int64  `json:"fiados"`
		Plan              string `json:"plan"`
		DiasDesdeRegistro int    `json:"dias_desde_registro"`
	}
	for i := range resp.Tiendas {
		if resp.Tiendas[i].Tienda == "Melons & Cherries" {
			melons = &resp.Tiendas[i]
		}
	}
	require.NotNil(t, melons)
	assert.EqualValues(t, 2, melons.Productos)
	assert.EqualValues(t, 0, melons.VentasTotal)
	assert.Empty(t, melons.UltimaVenta)
}
