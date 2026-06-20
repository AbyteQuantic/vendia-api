// Spec: specs/075-proveedores-b2b/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupplierHarvestAlerts(t *testing.T) {
	db := setupNearbyDB(t)
	// Proveedor con ubicación + una tienda cercana.
	mkTenant(t, db, "prov1", "[SEED] El Tomate", []string{"proveedor_agricola"}, 4.345, -74.365)
	mkTenant(t, db, "store1", "Tienda Rosa", []string{"tienda_barrio"}, 4.341, -74.360)

	soon := time.Now().AddDate(0, 0, 3).Format("2006-01-02") // vence en 3 días
	far := time.Now().AddDate(0, 0, 60).Format("2006-01-02")
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p1"}, TenantID: "prov1", Name: "Tomate", Price: 1, Stock: 30, ExpiryDate: &soon}).Error)
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p2"}, TenantID: "prov1", Name: "Papa", Price: 1, Stock: 40, ExpiryDate: &far}).Error)
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p3"}, TenantID: "prov1", Name: "Aceite", Price: 1, Stock: 50}).Error) // sin vencimiento

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "prov1"); c.Next() })
	r.GET("/supplier/harvest-alerts", handlers.SupplierHarvestAlerts(db))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/supplier/harvest-alerts?radius_km=5&days=7", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data []handlers.HarvestAlert `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// Solo el Tomate (por vencer); Papa (lejos) y Aceite (sin fecha) no.
	require.Len(t, resp.Data, 1)
	a := resp.Data[0]
	assert.Equal(t, "Tomate", a.Name)
	assert.LessOrEqual(t, a.DaysLeft, 3)
	assert.Equal(t, 1, a.NearbyStoreCount)
	assert.Contains(t, a.SuggestedMessage, "Tomate")
}
