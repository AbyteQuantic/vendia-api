// Spec: specs/075-proveedores-b2b/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupNearbyDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Tenant{}, &models.Product{}))
	return db
}

func mkTenant(t *testing.T, db *gorm.DB, id, name string, types []string, lat, lon float64) {
	t.Helper()
	require.NoError(t, db.Create(&models.Tenant{
		BaseModel: models.BaseModel{ID: id}, OwnerName: "o", Phone: "ph-" + id,
		PasswordHash: "x", BusinessName: name, BusinessTypes: types,
		SaleTypes: []string{"contado"}, Latitude: lat, Longitude: lon,
	}).Error)
}

func mountNearby(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, tenantID); c.Next() })
	r.GET("/suppliers/nearby", handlers.SuppliersNearby(db))
	return r
}

func getNearby(r *gin.Engine, url string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	r.ServeHTTP(w, req)
	return w
}

func TestSuppliersNearby(t *testing.T) {
	db := setupNearbyDB(t)
	// Tienda que llama (Fusagasugá centro).
	mkTenant(t, db, "store1", "Mi Tienda", []string{"tienda_barrio"}, 4.341, -74.360)
	// Proveedores: S1 muy cerca (~0.7km), S2 cerca (~1.3km), S3 lejos (excluir),
	// S4 sin ubicación (0,0 → excluir), T2 tienda cercana (no proveedor → excluir).
	mkTenant(t, db, "s1", "[SEED] El Tomate", []string{"proveedor_agricola"}, 4.345, -74.365)
	mkTenant(t, db, "s2", "El Granero", []string{"proveedor_mayorista"}, 4.350, -74.355)
	mkTenant(t, db, "s3", "Lejano", []string{"proveedor_agricola"}, 5.000, -75.000)
	mkTenant(t, db, "s4", "Sin GPS", []string{"proveedor_agricola"}, 0, 0)
	mkTenant(t, db, "t2", "Otra Tienda", []string{"tienda_barrio"}, 4.342, -74.361)
	// S1: 2 productos, 1 por vencer pronto.
	soon, far := "2026-06-22", "2026-12-31"
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p1"}, TenantID: "s1", Name: "Tomate", Price: 1, ExpiryDate: &soon}).Error)
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p2"}, TenantID: "s1", Name: "Cebolla", Price: 1, ExpiryDate: &far}).Error)
	// S2: 1 producto sin vencimiento.
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p3"}, TenantID: "s2", Name: "Aceite", Price: 1}).Error)

	r := mountNearby(db, "store1")
	w := getNearby(r, "/suppliers/nearby?radius_km=5")
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data []handlers.NearbySupplier `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// AC-03/04: solo S1 y S2, ordenados por distancia (S1 primero).
	require.Len(t, resp.Data, 2)
	assert.Equal(t, "s1", resp.Data[0].ID)
	assert.Equal(t, "s2", resp.Data[1].ID)
	assert.Less(t, resp.Data[0].DistanceKm, resp.Data[1].DistanceKm)
	// Conteos de S1.
	assert.Equal(t, 2, resp.Data[0].ProductCount)
	assert.Equal(t, 1, resp.Data[0].ExpiringSoonCount)
	// S3 (lejos), S4 (0,0) y t2 (no proveedor) NO aparecen.
	for _, s := range resp.Data {
		assert.NotContains(t, []string{"s3", "s4", "t2"}, s.ID)
	}
}

func TestSuppliersNearbyRadiusFilter(t *testing.T) {
	db := setupNearbyDB(t)
	mkTenant(t, db, "store1", "Mi Tienda", []string{"tienda_barrio"}, 4.341, -74.360)
	mkTenant(t, db, "s2", "El Granero", []string{"proveedor_mayorista"}, 4.350, -74.355) // ~1.3km
	r := mountNearby(db, "store1")
	// Radio 0.5km → s2 (~1.3km) queda fuera.
	w := getNearby(r, "/suppliers/nearby?radius_km=0.5")
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []handlers.NearbySupplier `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Data)
}

func TestSuppliersNearbyNoLocation(t *testing.T) {
	db := setupNearbyDB(t)
	mkTenant(t, db, "store1", "Mi Tienda", []string{"tienda_barrio"}, 0, 0) // sin ubicación
	r := mountNearby(db, "store1")
	w := getNearby(r, "/suppliers/nearby")
	// AC-05: 422 pidiendo fijar ubicación.
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}
