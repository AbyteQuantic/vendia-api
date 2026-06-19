// Spec: specs/067-planear-menu-ia-ux/spec.md
package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestGetMenuPlanToday_ReturnsTodaysDishes(t *testing.T) {
	db := setupMenuPlanTestDB()
	// Receta vinculada + plan con TODOS los días habilitados con esa receta,
	// para que "hoy" siempre resuelva sin depender del día real.
	db.Create(&models.Recipe{
		BaseModel:   models.BaseModel{ID: "r-1"},
		TenantID:    "t1",
		ProductName: "Bandeja paisa",
		SalePrice:   18000,
	})
	db.Create(&models.WeeklyMenuPlan{
		TenantID: "t1", BranchID: "", Days: allDaysWith("r-1"),
	})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(fakeTenant("t1"))
	r.GET("/menu-plan/today", GetMenuPlanToday(db))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/menu-plan/today", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	data := body["data"]
	assert.Equal(t, true, data["active"])
	assert.Equal(t, true, data["found"])
	assert.Equal(t, true, data["is_today"])

	items := data["items"].([]any)
	assert.Len(t, items, 1)
	first := items[0].(map[string]any)
	assert.Equal(t, "Bandeja paisa", first["name"])
}

func TestGetMenuPlanToday_NoPlanInactive(t *testing.T) {
	db := setupMenuPlanTestDB()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(fakeTenant("t1"))
	r.GET("/menu-plan/today", GetMenuPlanToday(db))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/menu-plan/today", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	assert.Equal(t, false, body["data"]["active"])
}
