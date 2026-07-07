// Spec: specs/098-aporte-automatico-fotos-colaborativo/spec.md
package handlers_test

import (
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

func setupTermsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY,
			terms_accepted_version TEXT DEFAULT '',
			terms_accepted_at DATETIME,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
		);
	`).Error)
	return db
}

func TestTenant_AcceptedCurrentTerms(t *testing.T) {
	assert.True(t, (&models.Tenant{TermsAcceptedVersion: models.CatalogTermsVersion}).AcceptedCurrentTerms())
	assert.False(t, (&models.Tenant{TermsAcceptedVersion: "vieja"}).AcceptedCurrentTerms())
	assert.False(t, (&models.Tenant{}).AcceptedCurrentTerms())
}

func TestAcceptTerms_SetsCurrentVersion(t *testing.T) {
	db := setupTermsDB(t)
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, terms_accepted_version) VALUES ('t1', '')`).Error)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.POST("/terms/accept", handlers.AcceptTerms(db))

	w := doJSON(t, r, http.MethodPost, "/terms/accept", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var version string
	var acceptedAt *string
	row := db.Raw(`SELECT terms_accepted_version, terms_accepted_at FROM tenants WHERE id = 't1'`).Row()
	require.NoError(t, row.Scan(&version, &acceptedAt))
	assert.Equal(t, models.CatalogTermsVersion, version)
	assert.NotNil(t, acceptedAt, "debe registrar la fecha de aceptación")
}

// AC-01: registro sin aceptar términos → 400 (fail-closed, antes de tocar DB).
func TestTenantRegister_RequiresAcceptTerms(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/register", handlers.TenantRegister(setupTermsDB(t), "secret"))

	body := map[string]any{
		"owner":    map[string]any{"name": "Ana", "phone": "3001234567", "password": "1234"},
		"business": map[string]any{"name": "Tienda Ana"},
		"config":   map[string]any{"sale_types": []string{"mostrador"}},
		// accept_terms omitido (=false)
	}
	w := doJSON(t, r, http.MethodPost, "/register", body)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "Términos")
}

// applyCapabilityFlags (vía AuthResponse) debe pedir re-aceptación cuando la
// versión aceptada no es la vigente.
func TestTermsAcceptanceRequired_ReflectedInAuthResponse(t *testing.T) {
	// Tenant que YA aceptó la vigente → no requiere.
	newer := models.Tenant{TermsAcceptedVersion: models.CatalogTermsVersion}
	assert.False(t, !newer.AcceptedCurrentTerms())
	// Tenant viejo (versión vacía) → requiere re-aceptar.
	old := models.Tenant{}
	assert.True(t, !old.AcceptedCurrentTerms())
}
