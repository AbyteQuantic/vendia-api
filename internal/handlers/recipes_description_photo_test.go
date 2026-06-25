// Spec: specs/078-centro-tareas-unificado/spec.md
//
// Reportes fundador 2026-06-25:
//  #2 — la foto del plato no aparecía en el catálogo online: al COMPLETAR un
//       plato importado (link_product_id) no se copiaba photo_url al Product.
//  #3 — la descripción del plato no persistía: UpdateRecipe la ignoraba.
// La descripción + foto que lee el catálogo viven en el Product vinculado.
package handlers_test

import (
	"bytes"
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
	"gorm.io/gorm"
)

func mountRecipeFull(t *testing.T, tenantID string) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db := setupRecipeHandlerDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/recipes", handlers.CreateRecipe(db))
	r.PATCH("/recipes/:uuid", handlers.UpdateRecipe(db))
	r.GET("/recipes", handlers.ListRecipes(db))
	return r, db
}

// #2 — completar un plato importado copia foto Y descripción al Product, así el
// catálogo (que lee product.photo_url / description) las muestra.
func TestCreateRecipe_LinkProduct_CopiesPhotoAndDescriptionToProduct(t *testing.T) {
	const tenant = "tenant-link"
	r, db := mountRecipeFull(t, tenant)

	// Plato importado INCOMPLETO (is_menu_item, sin receta, sin foto/desc).
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "prod-limonada"}, TenantID: tenant,
		Name: "Jarra de limonada grande", IsMenuItem: true, Price: 8000,
	}).Error)
	ins := seedInsumo(t, db, tenant, "ins-limon", "Limón", 200)

	body, _ := json.Marshal(map[string]any{
		"link_product_id": "prod-limonada",
		"product_name":    "Jarra de limonada grande",
		"sale_price":      8000,
		"photo_url":       "https://r2.vendia.co/limonada.webp",
		"description":     "Refrescante limonada natural de la casa.",
		"ingredients":     []map[string]any{{"ingredient_uuid": ins, "quantity": 2}},
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/recipes", bytes.NewReader(body)))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var prod models.Product
	require.NoError(t, db.Where("id = ?", "prod-limonada").First(&prod).Error)
	assert.Equal(t, "https://r2.vendia.co/limonada.webp", prod.PhotoURL, "foto al producto (catálogo)")
	assert.Equal(t, "Refrescante limonada natural de la casa.", prod.Description, "descripción al producto")
	assert.True(t, prod.IsRecipe, "queda ligado como receta")
}

// #3 — UpdateRecipe persiste la descripción en el Product vinculado.
func TestUpdateRecipe_PersistsDescriptionToProduct(t *testing.T) {
	const tenant = "tenant-upd"
	r, db := mountRecipeFull(t, tenant)
	ins := seedInsumo(t, db, tenant, "ins-1", "Arroz", 2900)

	// Crear un plato nuevo (genera Product con descripción inicial).
	createBody, _ := json.Marshal(map[string]any{
		"product_name": "Bandeja",
		"sale_price":   18000,
		"description":  "Original",
		"ingredients":  []map[string]any{{"ingredient_uuid": ins, "quantity": 1}},
	})
	cw := httptest.NewRecorder()
	r.ServeHTTP(cw, httptest.NewRequest(http.MethodPost, "/recipes", bytes.NewReader(createBody)))
	require.Equal(t, http.StatusCreated, cw.Code, cw.Body.String())
	var createdEnv struct {
		Data struct {
			ID        string `json:"id"`
			ProductID string `json:"product_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(cw.Body.Bytes(), &createdEnv))
	created := createdEnv.Data
	require.NotEmpty(t, created.ID)

	// Editar SOLO la descripción.
	updBody, _ := json.Marshal(map[string]any{"description": "Descripción nueva editada"})
	uw := httptest.NewRecorder()
	r.ServeHTTP(uw, httptest.NewRequest(http.MethodPatch, "/recipes/"+created.ID, bytes.NewReader(updBody)))
	require.Equal(t, http.StatusOK, uw.Code, uw.Body.String())

	// El Product vinculado tiene la descripción nueva (lo que lee el catálogo).
	var recipe models.Recipe
	require.NoError(t, db.Where("id = ?", created.ID).First(&recipe).Error)
	require.NotNil(t, recipe.ProductID)
	var prod models.Product
	require.NoError(t, db.Where("id = ?", *recipe.ProductID).First(&prod).Error)
	assert.Equal(t, "Descripción nueva editada", prod.Description)

	// Y ListRecipes la devuelve (round-trip → el Studio la precarga).
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, httptest.NewRequest(http.MethodGet, "/recipes", nil))
	require.Equal(t, http.StatusOK, lw.Code)
	var listResp struct {
		Data []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(lw.Body.Bytes(), &listResp))
	require.Len(t, listResp.Data, 1)
	assert.Equal(t, "Descripción nueva editada", listResp.Data[0].Description)
}
