// Spec: specs/077-compra-inteligente-insumos/spec.md
package handlers_test

import (
	"encoding/json"
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

func setupErrandDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.PurchaseErrand{}, &models.PurchaseErrandLine{}))
	return db
}

func mountErrands(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, "t1"); c.Next() })
	r.POST("/errands", handlers.CreateErrand(db))
	r.GET("/errands", handlers.ListErrands(db))
	r.PATCH("/errands/:id", handlers.UpdateErrandStatus(db))
	r.POST("/errands/match-today", handlers.MatchTodayErrand(db))
	return r
}

func TestErrandCreateListAndMatchToday(t *testing.T) {
	db := setupErrandDB(t)
	r := mountErrands(db)

	// UUIDs válidos como en uso real (la columna ingredient_id es uuid).
	const arrozID = "11111111-1111-1111-1111-111111111111"
	const papaID = "22222222-2222-2222-2222-222222222222"
	// Crear un mandado a un proveedor con teléfono → devuelve link WhatsApp.
	w := doJSON(t, r, http.MethodPost, "/errands", map[string]any{
		"assignee_type": "supplier", "assignee_name": "Mi mayorista", "assignee_phone": "3001112222",
		"lines": []map[string]any{
			{"ingredient_id": arrozID, "name": "Arroz", "unit": "kg", "qty": 3, "cost": 8400, "price_source": "manual"},
			{"ingredient_id": papaID, "name": "Papa", "unit": "kg", "qty": 2, "cost": 3000},
		},
	})
	require.Equal(t, http.StatusCreated, w.Code)
	var resp struct {
		Data struct {
			Errand      models.PurchaseErrand `json:"errand"`
			WhatsAppURL string                `json:"whatsapp_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.InDelta(t, 11400, resp.Data.Errand.TotalEstimated, 0.001) // 8400+3000
	assert.Equal(t, "pendiente", resp.Data.Errand.Status)
	assert.Contains(t, resp.Data.WhatsAppURL, "wa.me")
	id := resp.Data.Errand.ID

	// Listar → 1 mandado con 2 líneas.
	wl := doJSON(t, r, http.MethodGet, "/errands", nil)
	var list struct {
		Data []models.PurchaseErrand `json:"data"`
	}
	require.NoError(t, json.Unmarshal(wl.Body.Bytes(), &list))
	require.Len(t, list.Data, 1)
	require.Len(t, list.Data[0].Lines, 2)

	// match-today con los MISMOS insumos → lo encuentra (reenviar).
	wm := doJSON(t, r, http.MethodPost, "/errands/match-today", map[string]any{
		"ingredient_ids": []string{papaID, arrozID}, // distinto orden, mismo conjunto
	})
	var match struct {
		Data *models.PurchaseErrand `json:"data"`
	}
	require.NoError(t, json.Unmarshal(wm.Body.Bytes(), &match))
	require.NotNil(t, match.Data)
	assert.Equal(t, id, match.Data.ID)

	// match-today con OTRO conjunto → no encuentra.
	wm2 := doJSON(t, r, http.MethodPost, "/errands/match-today", map[string]any{
		"ingredient_ids": []string{arrozID},
	})
	var match2 struct {
		Data *models.PurchaseErrand `json:"data"`
	}
	require.NoError(t, json.Unmarshal(wm2.Body.Bytes(), &match2))
	assert.Nil(t, match2.Data)

	// Marcar comprado → estado cambia + closed_at.
	wu := doJSON(t, r, http.MethodPatch, "/errands/"+id, map[string]any{"status": "comprado"})
	assert.Equal(t, http.StatusOK, wu.Code)
}

func TestErrandCreate_NonUUIDIngredientIsNil(t *testing.T) {
	db := setupErrandDB(t)
	r := mountErrands(db)
	// ingredient_id no-UUID NO debe reventar: se guarda nil, el Name queda.
	w := doJSON(t, r, http.MethodPost, "/errands", map[string]any{
		"assignee_type": "self",
		"lines": []map[string]any{
			{"ingredient_id": "no-es-uuid", "name": "Arroz", "unit": "kg", "qty": 1, "cost": 100},
		},
	})
	require.Equal(t, http.StatusCreated, w.Code)
	var resp struct {
		Data struct {
			Errand models.PurchaseErrand `json:"errand"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Errand.Lines, 1)
	assert.Nil(t, resp.Data.Errand.Lines[0].IngredientID) // no-UUID → nil
	assert.Equal(t, "Arroz", resp.Data.Errand.Lines[0].Name)
}
