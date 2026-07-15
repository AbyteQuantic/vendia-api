// Spec: specs/105-hito-restaurante-comandas/spec.md — F3 (roles waiter|chef|courier).
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// AC-F3: el mapeo empleado→workspace lleva los roles nuevos tal cual y
// conserva el histórico (owner>admin>cashier).
func TestSpec105F3_WorkspaceRoleForEmployee(t *testing.T) {
	cases := []struct {
		name string
		emp  models.Employee
		want models.WorkspaceRole
	}{
		{"owner manda sobre todo", models.Employee{IsOwner: true, Role: models.RoleChef}, models.RoleOwner},
		{"admin", models.Employee{Role: models.RoleAdmin}, models.RoleWSAdmin},
		{"cashier", models.Employee{Role: models.RoleCashier}, models.RoleWSCashier},
		{"waiter viaja tal cual", models.Employee{Role: models.RoleWaiter}, models.RoleWSWaiter},
		{"chef viaja tal cual", models.Employee{Role: models.RoleChef}, models.RoleWSChef},
		{"courier viaja tal cual", models.Employee{Role: models.RoleCourier}, models.RoleWSCourier},
		{"rol vacío legacy → cashier", models.Employee{}, models.RoleWSCashier},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, models.WorkspaceRoleForEmployee(tc.emp))
		})
	}
}

// AC-F3: RequireBackOffice bloquea SOLO a los roles de piso nuevos.
// RETRO-COMPAT explícita del concilio: token sin rol = acceso total.
func TestSpec105F3_RequireBackOffice(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mount := func(role string) *httptest.ResponseRecorder {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			if role != "" {
				c.Set(middleware.RoleKey, role)
			}
			c.Next()
		})
		r.GET("/reports", middleware.RequireBackOffice(), func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reports", nil))
		return w
	}

	for _, blocked := range []string{"waiter", "chef", "courier"} {
		assert.Equal(t, http.StatusForbidden, mount(blocked).Code,
			"%s no ve reportes ni configuración", blocked)
	}
	for _, allowed := range []string{"", "owner", "admin", "cashier"} {
		assert.Equal(t, http.StatusOK, mount(allowed).Code,
			"rol %q conserva acceso (retro-compat)", allowed)
	}
}

// AC-F3: el enum cerrado acepta los roles nuevos y rechaza basura.
func TestSpec105F3_IsValidEmployeeRole(t *testing.T) {
	for _, ok := range []models.EmployeeRole{
		models.RoleAdmin, models.RoleCashier, models.RoleWaiter,
		models.RoleChef, models.RoleCourier,
	} {
		assert.True(t, models.IsValidEmployeeRole(ok), string(ok))
	}
	assert.False(t, models.IsValidEmployeeRole("gerente"))
	assert.False(t, models.IsValidEmployeeRole(""))
}

// AC-F3: enable_waiter_charge jamás es type-implied (default OFF incluso
// para restaurantes) — es decisión explícita del dueño.
func TestSpec105F3_WaiterChargeNoTypeImplied(t *testing.T) {
	flags := models.DefaultFeatureFlags(
		[]string{models.BusinessTypeRestaurante}, models.CapabilityToggles{})
	assert.False(t, flags.EnableWaiterCharge,
		"restaurante NO implica mesero-cobra; lo decide el dueño")
}
