// Spec: specs/072-captura-ubicacion-gps-osm/spec.md
package handlers_test

import (
	"errors"
	"net/http"
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

type fakeGeocoder struct {
	label, city string
	err         error
}

func (f fakeGeocoder) Reverse(lat, lng float64) (string, string, error) {
	return f.label, f.city, f.err
}

func setupLocDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Tenant{}))
	require.NoError(t, db.Create(&models.Tenant{
		BaseModel: models.BaseModel{ID: "t1"}, OwnerName: "o", Phone: "p", PasswordHash: "x",
		BusinessName: "Mi Tienda", SaleTypes: []string{"contado"},
	}).Error)
	return db
}

func mountLoc(db *gorm.DB, geo *fakeGeocoder) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	if geo == nil {
		r.PATCH("/store/location", handlers.UpdateStoreLocation(db, nil))
	} else {
		r.PATCH("/store/location", handlers.UpdateStoreLocation(db, geo))
	}
	return r
}

func TestUpdateStoreLocation_SavesCity(t *testing.T) {
	db := setupLocDB(t)
	r := mountLoc(db, &fakeGeocoder{label: "Calle 8, Fusagasugá, Cundinamarca", city: "Fusagasugá"})
	w := doJSON(t, r, http.MethodPatch, "/store/location",
		map[string]any{"latitude": 4.345, "longitude": -74.365, "accuracy": 12, "references": "portón verde"})
	require.Equal(t, http.StatusOK, w.Code)

	var saved models.Tenant
	db.First(&saved, "id = ?", "t1")
	assert.InDelta(t, 4.345, saved.Latitude, 0.0001)
	assert.Equal(t, "Fusagasugá", saved.City)
	assert.Equal(t, "portón verde", saved.LocationReferences)
	assert.Equal(t, "Calle 8, Fusagasugá, Cundinamarca", saved.Address) // address vacía → se llena
}

func TestUpdateStoreLocation_ClientCityWins(t *testing.T) {
	db := setupLocDB(t)
	// Geocoder de servidor devuelve otra cosa; debe ganar la ciudad del cliente.
	r := mountLoc(db, &fakeGeocoder{city: "OtraCiudad"})
	w := doJSON(t, r, http.MethodPatch, "/store/location",
		map[string]any{"latitude": 4.34, "longitude": -74.36, "city": "Fusagasugá"})
	require.Equal(t, http.StatusOK, w.Code)
	var saved models.Tenant
	db.First(&saved, "id = ?", "t1")
	assert.Equal(t, "Fusagasugá", saved.City)
}

func TestUpdateStoreLocation_RejectsZeroZero(t *testing.T) {
	db := setupLocDB(t)
	r := mountLoc(db, &fakeGeocoder{})
	w := doJSON(t, r, http.MethodPatch, "/store/location",
		map[string]any{"latitude": 0, "longitude": 0})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

func TestUpdateStoreLocation_GeocoderFailStillSaves(t *testing.T) {
	db := setupLocDB(t)
	r := mountLoc(db, &fakeGeocoder{err: errors.New("nominatim down")})
	w := doJSON(t, r, http.MethodPatch, "/store/location",
		map[string]any{"latitude": 4.34, "longitude": -74.36})
	require.Equal(t, http.StatusOK, w.Code) // guarda lat/long aunque falle el geocoder
	var saved models.Tenant
	db.First(&saved, "id = ?", "t1")
	assert.InDelta(t, 4.34, saved.Latitude, 0.0001)
	assert.Equal(t, "", saved.City) // sin ciudad, pero no rompe
}
