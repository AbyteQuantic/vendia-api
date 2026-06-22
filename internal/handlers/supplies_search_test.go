// Spec: specs/077-compra-inteligente-insumos/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSupplySearch_FindsRelevantChainProducts(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.IngredientPrice{}, &models.Tenant{}, &models.ChainPrice{}))
	mk := func(name, cat string, price float64) {
		require.NoError(t, db.Create(&models.ChainPrice{
			Chain: "exito", RawName: name, NormalizedName: services.NormalizeText(name), Category: cat,
			Price: price, ScrapedAt: time.Now(),
		}).Error)
	}
	mk("Aguacate Hass Und", "Verduras", 5390)
	mk("Set Llamadientes Aguacate", "Rasca encías", 50910) // ruido → filtrado
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.GET("/supplies/search", handlers.SupplySearch(db))
	w := doJSON(t, r, http.MethodGet, "/supplies/search?q=aguacate&unit=unidad&shortfall=3", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Options []handlers.PriceOption `json:"options"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.GreaterOrEqual(t, len(resp.Data.Options), 1)
	assert.Equal(t, "Aguacate Hass Und", resp.Data.Options[0].Label)
	for _, o := range resp.Data.Options {
		assert.NotContains(t, o.Label, "Llamadientes") // el mordedor NO aparece
	}
}

func TestSupplySearch_ShortQueryEmpty(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.IngredientPrice{}, &models.Tenant{}, &models.ChainPrice{}))
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.GET("/supplies/search", handlers.SupplySearch(db))
	w := doJSON(t, r, http.MethodGet, "/supplies/search?q=a", nil)
	require.Equal(t, http.StatusOK, w.Code)
}
