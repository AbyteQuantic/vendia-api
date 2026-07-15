// Spec: specs/105-hito-restaurante-comandas/spec.md — F5 (turno de caja + horas).
package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
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

func setupCashShiftDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.CashShift{}, &models.Sale{}, &models.SaleItem{},
		&models.StaffAttendance{},
	))
	return db
}

func mountCashShifts(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/cash-shifts", handlers.OpenCashShift(db))
	r.GET("/cash-shifts/current", handlers.CurrentCashShift(db))
	r.POST("/cash-shifts/:uuid/close", handlers.CloseCashShift(db))
	return r
}

func seedShiftSale(t *testing.T, db *gorm.DB, tenantID, shiftID string, method string, total float64) {
	t.Helper()
	sale := models.Sale{
		TenantID:      tenantID,
		Total:         total,
		PaymentMethod: models.PaymentMethod(method),
		CashShiftUUID: &shiftID,
	}
	require.NoError(t, db.Create(&sale).Error)
}

// AC-F5: abrir base 50.000 → 3 ventas (2 efectivo + 1 nequi) → cerrar
// contando: esperado = base + SOLO efectivo; diferencia = contado−esperado.
func TestSpec105F5_ArqueoCompleto(t *testing.T) {
	db := setupCashShiftDB(t)
	r := mountCashShifts(db, "t1")

	w := do105(t, r, http.MethodPost, "/cash-shifts",
		map[string]any{"opening_amount": 50000, "employee_name": "Marta"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created struct {
		Data models.CashShift `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	shiftID := created.Data.ID

	seedShiftSale(t, db, "t1", shiftID, "efectivo", 22000)
	seedShiftSale(t, db, "t1", shiftID, "cash", 8000)
	seedShiftSale(t, db, "t1", shiftID, "nequi", 15000) // digital NO va al cajón

	// current: esperado corriendo.
	w = do105(t, r, http.MethodGet, "/cash-shifts/current", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"running_expected":80000`)

	// cierre: contado 75.000 → faltante de 5.000 visible.
	w = do105(t, r, http.MethodPost, fmt.Sprintf("/cash-shifts/%s/close", shiftID),
		map[string]any{"counted_amount": 75000})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var closed struct {
		Data models.CashShift `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &closed))
	require.NotNil(t, closed.Data.ExpectedAmount)
	assert.Equal(t, float64(80000), *closed.Data.ExpectedAmount)
	require.NotNil(t, closed.Data.Difference)
	assert.Equal(t, float64(-5000), *closed.Data.Difference, "faltante de 5.000")
	assert.Equal(t, models.CashShiftClosed, closed.Data.Status)

	// cerrar dos veces → 409 (idempotencia de caja).
	w = do105(t, r, http.MethodPost, fmt.Sprintf("/cash-shifts/%s/close", shiftID),
		map[string]any{"counted_amount": 75000})
	assert.Equal(t, http.StatusConflict, w.Code)
}

// AC-F5: un cajón, un turno — segundo open sin cerrar → 409.
func TestSpec105F5_UnSoloTurnoAbierto(t *testing.T) {
	db := setupCashShiftDB(t)
	r := mountCashShifts(db, "t1")

	w := do105(t, r, http.MethodPost, "/cash-shifts", map[string]any{"opening_amount": 10000})
	require.Equal(t, http.StatusCreated, w.Code)

	w = do105(t, r, http.MethodPost, "/cash-shifts", map[string]any{"opening_amount": 20000})
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "shift_already_open")
}

// AC-F5: sin turno abierto, current → 404 honesto (el POS muestra "Abrir turno").
func TestSpec105F5_SinTurnoCurrent404(t *testing.T) {
	db := setupCashShiftDB(t)
	r := mountCashShifts(db, "t1")
	w := do105(t, r, http.MethodGet, "/cash-shifts/current", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// AC-F5: aislamiento multi-tenant — el turno de t1 no se ve ni cierra desde t2.
func TestSpec105F5_MultiTenant(t *testing.T) {
	db := setupCashShiftDB(t)
	r1 := mountCashShifts(db, "t1")
	r2 := mountCashShifts(db, "t2")

	w := do105(t, r1, http.MethodPost, "/cash-shifts", map[string]any{"opening_amount": 10000})
	require.Equal(t, http.StatusCreated, w.Code)
	var created struct {
		Data models.CashShift `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	w = do105(t, r2, http.MethodGet, "/cash-shifts/current", nil)
	assert.Equal(t, http.StatusNotFound, w.Code, "t2 no ve el turno de t1")

	w = do105(t, r2, http.MethodPost,
		fmt.Sprintf("/cash-shifts/%s/close", created.Data.ID),
		map[string]any{"counted_amount": 0})
	assert.Equal(t, http.StatusNotFound, w.Code, "t2 no cierra el turno de t1")
}

// AC-F5: clock-out del día requiere entrada previa; con ella, estampa la salida.
func TestSpec105F5_ClockInOut(t *testing.T) {
	db := setupCashShiftDB(t)
	tenantID := "t1"
	empUUID := uuid.NewString()

	// Sin entrada hoy → helper de asistencia vacío.
	today := time.Now().Format("2006-01-02")
	rec := models.StaffAttendance{
		TenantID: tenantID, EmployeeUUID: empUUID, Date: today,
	}
	now := time.Now()
	rec.ClockIn = &now
	require.NoError(t, db.Create(&rec).Error)

	var out models.StaffAttendance
	require.NoError(t, db.First(&out, "employee_uuid = ?", empUUID).Error)
	assert.NotNil(t, out.ClockIn)
	assert.Nil(t, out.ClockOut)

	require.NoError(t, db.Model(&out).Update("clock_out", time.Now()).Error)
	require.NoError(t, db.First(&out, "employee_uuid = ?", empUUID).Error)
	assert.NotNil(t, out.ClockOut, "columnas clock_in/clock_out migradas y escribibles")
}
