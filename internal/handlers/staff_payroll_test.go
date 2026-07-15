// Spec: specs/084-peluqueria-salon/spec.md
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupPayrollDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	stmts := []string{
		`CREATE TABLE tenants (id TEXT PRIMARY KEY, deleted_at DATETIME, created_at DATETIME)`,
		`CREATE TABLE employees (id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL, branch_id TEXT, name TEXT DEFAULT '',
			phone TEXT DEFAULT '', pin TEXT DEFAULT '', role TEXT DEFAULT 'cashier',
			password_hash TEXT DEFAULT '', is_owner INTEGER DEFAULT 0, is_active INTEGER DEFAULT 1,
			photo_url TEXT DEFAULT '')`,
		`CREATE TABLE sales (id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL, branch_id TEXT, employee_uuid TEXT,
			employee_name TEXT DEFAULT '', total REAL DEFAULT 0, tax_amount REAL DEFAULT 0,
			tip_amount REAL DEFAULT 0, payment_method TEXT DEFAULT 'cash',
			payment_status TEXT DEFAULT 'COMPLETED', source TEXT DEFAULT 'POS', price_tier TEXT DEFAULT 'retail', cash_shift_uuid TEXT)`,
		`CREATE TABLE sale_items (id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, sale_id TEXT NOT NULL, product_id TEXT, name TEXT DEFAULT '',
			price REAL DEFAULT 0, quantity INTEGER DEFAULT 1, subtotal REAL DEFAULT 0,
			is_container_charge INTEGER DEFAULT 0, is_service INTEGER DEFAULT 0,
			custom_description TEXT DEFAULT '', custom_unit_price REAL DEFAULT 0,
			employee_uuid TEXT, employee_name TEXT DEFAULT '', pay_basis TEXT DEFAULT 'none',
			commission_pct REAL, commission_amount REAL DEFAULT 0)`,
		`CREATE TABLE branches (id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL, name TEXT, is_active INTEGER DEFAULT 1)`,
	}
	for _, s := range stmts {
		require.NoError(t, db.Exec(s).Error)
	}
	require.NoError(t, db.AutoMigrate(&models.EmployeePayConfig{}, &models.EmployeePayout{},
		&models.StaffAttendance{}))
	// Índice parcial de idempotencia de liquidación (espeja ledger_constraints).
	require.NoError(t, db.Exec(
		`CREATE UNIQUE INDEX uq_payout_liquidacion_period ON employee_payouts
		 (tenant_id, employee_uuid, period_start, period_end)
		 WHERE status = 'paid' AND kind = 'liquidacion' AND deleted_at IS NULL`).Error)
	return db
}

func mountPayroll(db *gorm.DB, tenantID string) *gin.Engine {
	return mountPayrollRole(db, tenantID, "owner")
}

func mountPayrollRole(db *gorm.DB, tenantID, role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Set(middleware.RoleKey, role)
		c.Next()
	})
	r.GET("/employees/:uuid/pay-config", handlers.GetEmployeePayConfig(db))
	r.PUT("/employees/:uuid/pay-config", handlers.UpsertEmployeePayConfig(db))
	r.GET("/payouts/liquidation", handlers.GetLiquidation(db))
	r.POST("/payouts", handlers.CreatePayout(db))
	r.GET("/payouts", handlers.ListPayouts(db))
	r.POST("/payouts/:id/void", handlers.VoidPayout(db))
	r.POST("/staff-attendance", handlers.MarkAttendance(db))
	return r
}

func seedServiceSale(t *testing.T, db *gorm.DB, tenantID, empID, empName string,
	tax, tip float64, lines [][2]float64) string {
	t.Helper()
	saleID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO sales (id, tenant_id, employee_uuid, employee_name, total, tax_amount, tip_amount, payment_status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'COMPLETED', ?)`,
		saleID, tenantID, empID, empName, 0, tax, tip, time.Now()).Error)
	for _, l := range lines { // [subtotal, commissionAmount]
		require.NoError(t, db.Exec(
			`INSERT INTO sale_items (id, sale_id, name, price, quantity, subtotal, is_service, employee_uuid, employee_name, pay_basis, commission_amount, created_at)
			 VALUES (?, ?, 'Corte', ?, 1, ?, 1, ?, ?, 'commission', ?, ?)`,
			uuid.NewString(), saleID, l[0], l[0], empID, empName, l[1], time.Now()).Error)
	}
	return saleID
}

// PUT pay-config valida el modelo y exige su parámetro; GET devuelve la activa.
func TestPayConfig_UpsertAndGet(t *testing.T) {
	db := setupPayrollDB(t)
	tenantID := uuid.NewString()
	empID := uuid.NewString()
	r := mountPayroll(db, tenantID)

	// Falta el porcentaje → 400.
	w := doJSON(t, r, http.MethodPut, "/employees/"+empID+"/pay-config",
		map[string]any{"pay_model": "commission"})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// Con porcentaje → 200.
	w = doJSON(t, r, http.MethodPut, "/employees/"+empID+"/pay-config",
		map[string]any{"pay_model": "commission", "commission_pct": 40})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// GET devuelve la config activa.
	g := httptest.NewRequest(http.MethodGet, "/employees/"+empID+"/pay-config", nil)
	gw := httptest.NewRecorder()
	r.ServeHTTP(gw, g)
	require.Equal(t, http.StatusOK, gw.Code)
	assert.Contains(t, gw.Body.String(), "commission")

	// Un segundo PUT desactiva el anterior → sigue habiendo UNA sola activa.
	doJSON(t, r, http.MethodPut, "/employees/"+empID+"/pay-config",
		map[string]any{"pay_model": "fixed_per_job", "fixed_per_job": 8000})
	var active int64
	db.Model(&models.EmployeePayConfig{}).
		Where("tenant_id = ? AND employee_uuid = ? AND is_active = ?", tenantID, empID, true).Count(&active)
	assert.Equal(t, int64(1), active, "solo una config activa por profesional")
}

// Liquidación: suma la comisión CONGELADA por profesional; prorratea la propina.
func TestLiquidation_SumsFrozenCommission(t *testing.T) {
	db := setupPayrollDB(t)
	tenantID := uuid.NewString()
	empID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO employees (id, tenant_id, name, created_at) VALUES (?, ?, 'Ana', ?)`,
		empID, tenantID, time.Now()).Error)
	// Config comisión (para PayModel del contexto).
	pct := 40.0
	require.NoError(t, db.Create(&models.EmployeePayConfig{
		BaseModel: models.BaseModel{ID: uuid.NewString()}, TenantID: tenantID, EmployeeUUID: empID,
		PayModel: models.PayModelCommission, CommissionPct: &pct, TipRate: 1,
		EffectiveFrom: time.Now().Add(-time.Hour), IsActive: true,
	}).Error)
	// Venta con 2 servicios (subtotal, comisión congelada) + propina 1500, sin IVA.
	seedServiceSale(t, db, tenantID, empID, "Ana", 0, 1500, [][2]float64{{10000, 4000}, {6000, 2400}})

	r := mountPayroll(db, tenantID)
	g := httptest.NewRequest(http.MethodGet, "/payouts/liquidation", nil)
	gw := httptest.NewRecorder()
	r.ServeHTTP(gw, g)
	require.Equal(t, http.StatusOK, gw.Code, gw.Body.String())
	body := gw.Body.String()
	assert.Contains(t, body, "Ana")
	assert.Contains(t, body, `"commission_amount":6400`) // 4000+2400 congelado
	assert.Contains(t, body, `"tip_amount":1500`)        // propina prorrateada y sumada
	assert.Contains(t, body, `"net_payout":7900`)        // 6400 + 1500
}

// Asistencia idempotente: marcar dos veces el mismo día = una sola fila.
func TestMarkAttendance_Idempotent(t *testing.T) {
	db := setupPayrollDB(t)
	tenantID := uuid.NewString()
	empID := uuid.NewString()
	r := mountPayroll(db, tenantID)
	body := map[string]any{"employee_uuid": empID, "date": "2026-06-27"}
	require.Equal(t, http.StatusOK, doJSON(t, r, http.MethodPost, "/staff-attendance", body).Code)
	require.Equal(t, http.StatusOK, doJSON(t, r, http.MethodPost, "/staff-attendance", body).Code)
	var n int64
	db.Model(&models.StaffAttendance{}).Where("tenant_id = ? AND employee_uuid = ?", tenantID, empID).Count(&n)
	assert.Equal(t, int64(1), n, "una sola asistencia por día")
}

// Rol-gating: un cajero NO puede registrar pagos ni cambiar el esquema (403).
func TestPayroll_CashierForbidden(t *testing.T) {
	db := setupPayrollDB(t)
	tenantID := uuid.NewString()
	empID := uuid.NewString()
	r := mountPayrollRole(db, tenantID, "cashier")

	cfg := doJSON(t, r, http.MethodPut, "/employees/"+empID+"/pay-config",
		map[string]any{"pay_model": "commission", "commission_pct": 40})
	assert.Equal(t, http.StatusForbidden, cfg.Code)

	pay := doJSON(t, r, http.MethodPost, "/payouts", map[string]any{
		"employee_uuid": empID, "kind": "liquidacion",
		"period_start": time.Now().Format(time.RFC3339),
		"period_end":   time.Now().Format(time.RFC3339),
	})
	assert.Equal(t, http.StatusForbidden, pay.Code)
}

// Payout: registrar, listar, idempotencia de liquidación, anular.
func TestPayout_CreateListVoid(t *testing.T) {
	db := setupPayrollDB(t)
	tenantID := uuid.NewString()
	empID := uuid.NewString()
	r := mountPayroll(db, tenantID)
	ps := time.Now().AddDate(0, 0, -7).Format(time.RFC3339)
	pe := time.Now().Format(time.RFC3339)

	body := map[string]any{
		"employee_uuid": empID, "employee_name": "Ana", "kind": "liquidacion",
		"period_start": ps, "period_end": pe, "net_payout": 7900, "commission_amount": 6400,
		"tip_amount": 1500, "method": "efectivo",
	}
	w := doJSON(t, r, http.MethodPost, "/payouts", body)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	// Idempotencia: misma liquidación del mismo periodo → 409.
	w2 := doJSON(t, r, http.MethodPost, "/payouts", body)
	assert.Equal(t, http.StatusConflict, w2.Code, "no doble liquidación del mismo periodo")

	// Listar → 1.
	g := httptest.NewRequest(http.MethodGet, "/payouts", nil)
	gw := httptest.NewRecorder()
	r.ServeHTTP(gw, g)
	require.Equal(t, http.StatusOK, gw.Code)
	assert.Contains(t, gw.Body.String(), "Ana")

	// Anular.
	var row models.EmployeePayout
	require.NoError(t, db.First(&row).Error)
	vw := doJSON(t, r, http.MethodPost, "/payouts/"+row.ID+"/void", nil)
	require.Equal(t, http.StatusOK, vw.Code, vw.Body.String())
	db.First(&row, "id = ?", row.ID)
	assert.Equal(t, models.PayoutStatusVoid, row.Status)
}
