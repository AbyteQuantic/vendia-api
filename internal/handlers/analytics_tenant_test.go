// Spec: specs/005-fixes-regresion-360/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupSalesByEmployeeDB migrates the schema SalesByEmployee touches:
// Sale (the rows it groups) and Branch (ResolveBranchScope's ownership
// check). The Sale model carries Postgres-only column defaults that
// sqlite tolerates fine for a SELECT-only test.
func setupSalesByEmployeeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Sale{},
		&models.Branch{},
	))
	return db
}

func mountSalesByEmployeeHandler(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.GET("/analytics/sales-by-employee", handlers.SalesByEmployee(db))
	return r
}

// FR-01 / AC-01 — with sales from two different employees today the
// endpoint must return a list with the per-employee totals, never
// `null`. The bug compared the `employee_uuid` UUID column against an
// empty string, which on Postgres raises 22P02 and GORM silences the
// error, leaving `data` null.
func TestSalesByEmployee_ReturnsPerEmployeeTotals(t *testing.T) {
	db := setupSalesByEmployeeDB(t)
	tenantID := "tenant-sbe"

	empA := "a1000000-0000-4000-8000-000000000001"
	empB := "b1000000-0000-4000-8000-000000000001"
	now := time.Now()

	require.NoError(t, db.Create(&models.Sale{
		BaseModel: models.BaseModel{ID: "5a1e0000-0000-4000-8000-000000000001", CreatedAt: now},
		TenantID:  tenantID, EmployeeUUID: &empA, EmployeeName: "Ana",
		Total: 10000, PaymentMethod: models.PaymentCash,
	}).Error)
	require.NoError(t, db.Create(&models.Sale{
		BaseModel: models.BaseModel{ID: "5a1e0000-0000-4000-8000-000000000002", CreatedAt: now},
		TenantID:  tenantID, EmployeeUUID: &empA, EmployeeName: "Ana",
		Total: 5000, PaymentMethod: models.PaymentCash,
	}).Error)
	require.NoError(t, db.Create(&models.Sale{
		BaseModel: models.BaseModel{ID: "5a1e0000-0000-4000-8000-000000000003", CreatedAt: now},
		TenantID:  tenantID, EmployeeUUID: &empB, EmployeeName: "Beto",
		Total: 8000, PaymentMethod: models.PaymentCash,
	}).Error)

	r := mountSalesByEmployeeHandler(db, tenantID)
	w := doJSON(t, r, http.MethodGet, "/analytics/sales-by-employee", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data []struct {
			EmployeeUUID string  `json:"employee_uuid"`
			EmployeeName string  `json:"employee_name"`
			SaleCount    int64   `json:"sale_count"`
			TotalAmount  float64 `json:"total_amount"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data, "data must NOT be null (FR-01)")
	require.Len(t, resp.Data, 2, "one row per employee")

	byUUID := map[string]struct {
		count int64
		total float64
	}{}
	for _, row := range resp.Data {
		byUUID[row.EmployeeUUID] = struct {
			count int64
			total float64
		}{row.SaleCount, row.TotalAmount}
	}
	assert.Equal(t, int64(2), byUUID[empA].count, "Ana hizo 2 ventas")
	assert.Equal(t, float64(15000), byUUID[empA].total, "Ana: 10000 + 5000")
	assert.Equal(t, int64(1), byUUID[empB].count, "Beto hizo 1 venta")
	assert.Equal(t, float64(8000), byUUID[empB].total, "Beto: 8000")
}

// FR-01 — a sale with no employee attached (employee_uuid IS NULL)
// must be excluded from the ranking instead of crashing the query or
// landing in a bogus empty-UUID bucket.
func TestSalesByEmployee_ExcludesSalesWithoutEmployee(t *testing.T) {
	db := setupSalesByEmployeeDB(t)
	tenantID := "tenant-sbe-null"

	empA := "a2000000-0000-4000-8000-000000000001"
	now := time.Now()

	require.NoError(t, db.Create(&models.Sale{
		BaseModel: models.BaseModel{ID: "5a2e0000-0000-4000-8000-000000000001", CreatedAt: now},
		TenantID:  tenantID, EmployeeUUID: &empA, EmployeeName: "Ana",
		Total: 12000, PaymentMethod: models.PaymentCash,
	}).Error)
	// A sale with no employee — EmployeeUUID nil. Must not appear.
	require.NoError(t, db.Create(&models.Sale{
		BaseModel: models.BaseModel{ID: "5a2e0000-0000-4000-8000-000000000002", CreatedAt: now},
		TenantID:  tenantID, EmployeeUUID: nil, EmployeeName: "",
		Total: 9000, PaymentMethod: models.PaymentCash,
	}).Error)

	r := mountSalesByEmployeeHandler(db, tenantID)
	w := doJSON(t, r, http.MethodGet, "/analytics/sales-by-employee", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data []struct {
			EmployeeUUID string  `json:"employee_uuid"`
			TotalAmount  float64 `json:"total_amount"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1, "only the sale with an employee is counted")
	assert.Equal(t, empA, resp.Data[0].EmployeeUUID)
	assert.Equal(t, float64(12000), resp.Data[0].TotalAmount)
}
