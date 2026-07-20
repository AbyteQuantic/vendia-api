// Spec: specs/107-dashboard-v2-resumen/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
)

func summaryRouter(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	inject := func(c *gin.Context) { c.Set(middleware.TenantIDKey, tenantID); c.Next() }
	r.GET("/summary", inject, handlers.DashboardSummary(db))
	r.GET("/analytics", inject, handlers.AnalyticsDashboard(db))
	return r
}

func getJSON(t *testing.T, r *gin.Engine, path string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return out.Data
}

func seedSummaryTenant(t *testing.T, db *gorm.DB) models.Tenant {
	t.Helper()
	tenant := models.Tenant{
		OwnerName: "Sum", Phone: uniquePhone(), PasswordHash: "x",
		BusinessName: "Resumen SA", SaleTypes: []string{"products"},
	}
	require.NoError(t, db.Create(&tenant).Error)
	t.Cleanup(func() {
		for _, m := range []any{&models.CreditPayment{}, &models.CreditAccount{},
			&models.SaleItem{}, &models.Sale{}, &models.Product{},
			&models.OnlineOrder{}, &models.CashShift{}} {
			db.Unscoped().Where("tenant_id = ?", tenant.ID).Delete(m)
		}
		db.Unscoped().Delete(&tenant)
	})
	return tenant
}

func TestDashboardSummaryParityAndShape(t *testing.T) {
	db := setupTestDB(t)
	tenant := seedSummaryTenant(t, db)

	// Ventas de hoy: 2 (una con costo), y una de AYER que NO debe contar.
	now := time.Now()
	sale1 := models.Sale{TenantID: tenant.ID, Total: 38500, PaymentMethod: "cash"}
	require.NoError(t, db.Create(&sale1).Error)
	sale2 := models.Sale{TenantID: tenant.ID, Total: 20000, PaymentMethod: "nequi"}
	require.NoError(t, db.Create(&sale2).Error)
	old := models.Sale{TenantID: tenant.ID, Total: 99999, PaymentMethod: "cash"}
	require.NoError(t, db.Create(&old).Error)
	require.NoError(t, db.Model(&models.Sale{}).Where("id = ?", old.ID).
		Update("created_at", now.Add(-48*time.Hour)).Error)

	// Fiados: 2 aceptados con saldo (uno viejo) + 1 pagado (no cuenta).
	// Clientes reales primero (FK) y fiado_token uuid (columna type:uuid).
	mkCustomer := func(name string) string {
		cu := models.Customer{TenantID: tenant.ID, Name: name}
		require.NoError(t, db.Create(&cu).Error)
		t.Cleanup(func() { db.Unscoped().Delete(&cu) })
		return cu.ID
	}
	c1, c2, c3 := mkCustomer("Marta"), mkCustomer("Pedro"), mkCustomer("Luz")
	openA := models.CreditAccount{TenantID: tenant.ID, CustomerID: c1,
		TotalAmount: 30000, PaidAmount: 8000, Status: "partial",
		FiadoStatus: "accepted", FiadoToken: "aaaaaaa1-1111-4111-8111-111111111111"}
	require.NoError(t, db.Create(&openA).Error)
	require.NoError(t, db.Model(&models.CreditAccount{}).Where("id = ?", openA.ID).
		Update("created_at", now.Add(-12*24*time.Hour)).Error)
	openB := models.CreditAccount{TenantID: tenant.ID, CustomerID: c2,
		TotalAmount: 10000, PaidAmount: 0, Status: "open",
		FiadoStatus: "accepted", FiadoToken: "aaaaaaa2-2222-4222-8222-222222222222"}
	require.NoError(t, db.Create(&openB).Error)
	paid := models.CreditAccount{TenantID: tenant.ID, CustomerID: c3,
		TotalAmount: 5000, PaidAmount: 5000, Status: "paid",
		FiadoStatus: "accepted", FiadoToken: "aaaaaaa3-3333-4333-8333-333333333333"}
	require.NoError(t, db.Create(&paid).Error)

	// Stock bajo: 1 producto en umbral, 1 sano.
	low := models.Product{TenantID: tenant.ID, Name: "Arroz", Price: 3500,
		Stock: 1, MinStock: 3, IsAvailable: true}
	require.NoError(t, db.Create(&low).Error)
	ok := models.Product{TenantID: tenant.ID, Name: "Aceite", Price: 12000,
		Stock: 50, MinStock: 3, IsAvailable: true}
	require.NoError(t, db.Create(&ok).Error)

	// Pedido online pendiente + turno de caja abierto.
	oo := models.OnlineOrder{TenantID: tenant.ID, CustomerName: "Camilo",
		TotalAmount: 54000, Status: "pending"}
	require.NoError(t, db.Create(&oo).Error)
	shift := models.CashShift{TenantID: tenant.ID, Status: "open",
		OpeningAmount: 50000, OpenedAt: now.Add(-3 * time.Hour), OpenedByName: "Sum"}
	require.NoError(t, db.Create(&shift).Error)

	r := summaryRouter(db, tenant.ID)
	sum := getJSON(t, r, "/summary")
	ana := getJSON(t, r, "/analytics")

	// ── Paridad con el dashboard actual (Art. VII: misma fórmula) ──
	sales := sum["sales_today"].(map[string]any)
	assert.EqualValues(t, ana["total_sales_today"], sales["total"], "monto hoy = analytics")
	assert.EqualValues(t, ana["transaction_count"], sales["count"])
	assert.EqualValues(t, 58500, sales["total"], "solo las de hoy")

	recv := sum["receivables"].(map[string]any)
	assert.EqualValues(t, 32000, recv["total"], "30000-8000 + 10000")
	assert.EqualValues(t, 2, recv["debtors"])
	assert.GreaterOrEqual(t, recv["oldest_days"].(float64), float64(11))

	low2 := sum["low_stock"].(map[string]any)
	assert.EqualValues(t, ana["low_stock_count"], low2["count"])
	examples := low2["examples"].([]any)
	require.NotEmpty(t, examples)
	assert.Equal(t, "Arroz", examples[0])

	inprog := sum["in_progress"].(map[string]any)
	assert.EqualValues(t, 1, inprog["online"])

	shiftOut := sum["cash_shift"].(map[string]any)
	assert.Equal(t, true, shiftOut["open"])

	movs := sum["movements"].([]any)
	require.NotEmpty(t, movs)
	first := movs[0].(map[string]any)
	assert.Contains(t, []any{"sale", "online_order", "credit_payment"}, first["kind"])
	assert.NotEmpty(t, first["at"])

	assert.NotEmpty(t, sum["generated_at"])
	_, hasTasks := sum["tasks"].(map[string]any)
	assert.True(t, hasTasks, "counts de tareas presentes")
}

func TestDashboardSummaryTenantIsolation(t *testing.T) {
	db := setupTestDB(t)
	tenantA := seedSummaryTenant(t, db)
	tenantB := seedSummaryTenant(t, db)

	require.NoError(t, db.Create(&models.Sale{TenantID: tenantA.ID, Total: 77777,
		PaymentMethod: "cash"}).Error)

	sumB := getJSON(t, summaryRouter(db, tenantB.ID), "/summary")
	salesB := sumB["sales_today"].(map[string]any)
	assert.EqualValues(t, 0, salesB["total"], "B jamás ve ventas de A (AC-11)")
}

func TestDashboardSummaryEmptyTenant(t *testing.T) {
	// Tenant recién creado: todo en cero, sin errores (caso borde spec §9).
	db := setupTestDB(t)
	tenant := seedSummaryTenant(t, db)
	sum := getJSON(t, summaryRouter(db, tenant.ID), "/summary")
	assert.EqualValues(t, 0, sum["sales_today"].(map[string]any)["count"])
	assert.EqualValues(t, 0, sum["receivables"].(map[string]any)["total"])
	assert.Equal(t, false, sum["cash_shift"].(map[string]any)["open"])
	assert.Empty(t, sum["movements"])
}
