// Spec: specs/066-planear-menu/spec.md
package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupMenuPlanTestDB() *gorm.DB {
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	db.AutoMigrate(
		&models.Tenant{},
		&models.Product{},
		&models.Promotion{},
		&models.TenantPaymentMethod{},
		&models.TenantCatalogConfig{},
		&models.Recipe{},
		&models.WeeklyMenuPlan{},
		&models.MenuPlanOverride{},
	)
	return db
}

// fakeTenant inyecta el tenant_id en el contexto como lo haría el middleware
// de auth, para probar los handlers protegidos sin un JWT real.
func fakeTenant(tenantID string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(string(middleware.TenantIDKey), tenantID)
		c.Next()
	}
}

func TestMenuPlan_PutThenGet(t *testing.T) {
	db := setupMenuPlanTestDB()
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(fakeTenant("t1"))
	r.PUT("/menu-plan", UpsertMenuPlan(db))
	r.GET("/menu-plan", GetMenuPlan(db))

	body := `{"days":{"thu":{"enabled":true,"items":[{"recipe_uuid":"r1","planned_qty":12}]},"zzz":{"enabled":true,"items":[]}}}`
	req, _ := http.NewRequest(http.MethodPut, "/menu-plan", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	req2, _ := http.NewRequest(http.MethodGet, "/menu-plan", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)

	var res struct {
		Data struct {
			Days map[string]struct {
				Enabled bool `json:"enabled"`
				Items   []struct {
					RecipeUUID string `json:"recipe_uuid"`
					PlannedQty int    `json:"planned_qty"`
				} `json:"items"`
			} `json:"days"`
		} `json:"data"`
	}
	assert.NoError(t, json.Unmarshal(w2.Body.Bytes(), &res))
	assert.True(t, res.Data.Days["thu"].Enabled)
	assert.Len(t, res.Data.Days["thu"].Items, 1)
	assert.Equal(t, 12, res.Data.Days["thu"].Items[0].PlannedQty)
	// La clave de día inválida ("zzz") se descarta al sanear.
	_, hasInvalid := res.Data.Days["zzz"]
	assert.False(t, hasInvalid)
}

func TestMenuPlanOverride_PutListDelete(t *testing.T) {
	db := setupMenuPlanTestDB()
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(fakeTenant("t1"))
	r.PUT("/menu-plan/overrides", UpsertMenuPlanOverride(db))
	r.GET("/menu-plan/overrides", ListMenuPlanOverrides(db))
	r.DELETE("/menu-plan/overrides/:date", DeleteMenuPlanOverride(db))

	// Fecha lejana en el futuro para que ListOverrides (>= hoy) la incluya.
	body := `{"date":"2099-12-31","enabled":false,"items":[{"recipe_uuid":"r9","planned_qty":3}]}`
	req, _ := http.NewRequest(http.MethodPut, "/menu-plan/overrides", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	req2, _ := http.NewRequest(http.MethodGet, "/menu-plan/overrides", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	var listRes struct {
		Data []struct {
			Date    string `json:"date"`
			Enabled bool   `json:"enabled"`
		} `json:"data"`
	}
	assert.NoError(t, json.Unmarshal(w2.Body.Bytes(), &listRes))
	assert.Len(t, listRes.Data, 1)
	assert.Equal(t, "2099-12-31", listRes.Data[0].Date)
	assert.False(t, listRes.Data[0].Enabled)

	// Borrar y verificar que desaparece.
	reqD, _ := http.NewRequest(http.MethodDelete, "/menu-plan/overrides/2099-12-31", nil)
	wD := httptest.NewRecorder()
	r.ServeHTTP(wD, reqD)
	assert.Equal(t, http.StatusOK, wD.Code)

	req3, _ := http.NewRequest(http.MethodGet, "/menu-plan/overrides", nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	var after struct {
		Data []any `json:"data"`
	}
	assert.NoError(t, json.Unmarshal(w3.Body.Bytes(), &after))
	assert.Len(t, after.Data, 0)
}

func TestMenuPlanOverride_RejectsBadDate(t *testing.T) {
	db := setupMenuPlanTestDB()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(fakeTenant("t1"))
	r.PUT("/menu-plan/overrides", UpsertMenuPlanOverride(db))

	req, _ := http.NewRequest(http.MethodPut, "/menu-plan/overrides",
		strings.NewReader(`{"date":"18-06-2026","enabled":true,"items":[]}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// catalogProductsOf decodifica los productos del catálogo público.
func catalogProductsOf(t *testing.T, body []byte) ([]string, bool, string) {
	var res struct {
		Data struct {
			Products []struct {
				UUID string `json:"uuid"`
			} `json:"products"`
			MenuPlanActive bool   `json:"menu_plan_active"`
			MenuDayLabel   string `json:"menu_day_label"`
		} `json:"data"`
	}
	assert.NoError(t, json.Unmarshal(body, &res))
	ids := make([]string, 0, len(res.Data.Products))
	for _, p := range res.Data.Products {
		ids = append(ids, p.UUID)
	}
	return ids, res.Data.MenuPlanActive, res.Data.MenuDayLabel
}

func seedMenuCatalog(db *gorm.DB) {
	slug := "resto"
	db.Create(&models.Tenant{BaseModel: models.BaseModel{ID: "t1"}, BusinessName: "Resto", StoreSlug: &slug, IsDeliveryOpen: true})
	// Un producto normal + dos platos.
	db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p-normal"}, TenantID: "t1", Name: "Gaseosa", Price: 3000, Stock: 10})
	db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p-dish1"}, TenantID: "t1", Name: "Bandeja", Price: 18000, IsMenuItem: true})
	db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p-dish2"}, TenantID: "t1", Name: "Sancocho", Price: 16000, IsMenuItem: true})
	// Recetas que enlazan cada plato.
	pd1, pd2 := "p-dish1", "p-dish2"
	db.Create(&models.Recipe{BaseModel: models.BaseModel{ID: "r1"}, TenantID: "t1", ProductName: "Bandeja", SalePrice: 18000, ProductID: &pd1})
	db.Create(&models.Recipe{BaseModel: models.BaseModel{ID: "r2"}, TenantID: "t1", ProductName: "Sancocho", SalePrice: 16000, ProductID: &pd2})
}

func TestPublicCatalog_NoPlanShowsAllDishes(t *testing.T) {
	db := setupMenuPlanTestDB()
	gin.SetMode(gin.TestMode)
	seedMenuCatalog(db)

	r := gin.New()
	r.GET("/catalog/:slug", PublicCatalog(db))
	req, _ := http.NewRequest(http.MethodGet, "/catalog/resto", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	ids, active, _ := catalogProductsOf(t, w.Body.Bytes())
	assert.False(t, active) // sin plan = legacy
	assert.ElementsMatch(t, []string{"p-normal", "p-dish1", "p-dish2"}, ids)
}

func TestPublicCatalog_PlanFiltersDishesToToday(t *testing.T) {
	db := setupMenuPlanTestDB()
	gin.SetMode(gin.TestMode)
	seedMenuCatalog(db)

	// Plan con TODOS los días habilitados con SOLO r1 (p-dish1). Así el
	// resolvedor encuentra "hoy" sin depender de la fecha real → p-dish2 se
	// oculta, p-dish1 y el producto normal permanecen.
	allDays := `{"enabled":true,"items":[{"recipe_uuid":"r1","planned_qty":5}]}`
	days := `{"mon":` + allDays + `,"tue":` + allDays + `,"wed":` + allDays +
		`,"thu":` + allDays + `,"fri":` + allDays + `,"sat":` + allDays + `,"sun":` + allDays + `}`
	db.Create(&models.WeeklyMenuPlan{BaseModel: models.BaseModel{ID: "wp1"}, TenantID: "t1", Days: days})

	r := gin.New()
	r.GET("/catalog/:slug", PublicCatalog(db))
	req, _ := http.NewRequest(http.MethodGet, "/catalog/resto", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	ids, active, label := catalogProductsOf(t, w.Body.Bytes())
	assert.True(t, active)
	assert.Equal(t, "", label) // es hoy
	assert.ElementsMatch(t, []string{"p-normal", "p-dish1"}, ids)
	assert.NotContains(t, ids, "p-dish2")
}

func TestPublicCatalog_EmptyPlanHidesAllDishes(t *testing.T) {
	db := setupMenuPlanTestDB()
	gin.SetMode(gin.TestMode)
	seedMenuCatalog(db)

	// Plan existe pero todos los días apagados → sección de platos oculta,
	// el producto normal permanece (AC-13).
	db.Create(&models.WeeklyMenuPlan{BaseModel: models.BaseModel{ID: "wp1"}, TenantID: "t1", Days: `{}`})

	r := gin.New()
	r.GET("/catalog/:slug", PublicCatalog(db))
	req, _ := http.NewRequest(http.MethodGet, "/catalog/resto", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	ids, active, _ := catalogProductsOf(t, w.Body.Bytes())
	assert.True(t, active)
	assert.ElementsMatch(t, []string{"p-normal"}, ids)
}
