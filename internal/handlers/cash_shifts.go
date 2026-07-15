// Spec: specs/105-hito-restaurante-comandas/spec.md — F5 (turno de caja con arqueo).
package handlers

import (
	"errors"
	"net/http"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// cashInDrawer — métodos que dejan billetes en el cajón. Lo digital
// (nequi/daviplata/tarjeta/fiado) no cuenta para el arqueo físico.
var cashInDrawer = []string{"cash", "efectivo"}

// openShiftFor busca el turno abierto del tenant (scope de sede).
func openShiftFor(db *gorm.DB, tenantID string, branchID string) (*models.CashShift, error) {
	q := db.Where("tenant_id = ? AND status = ?", tenantID, models.CashShiftOpen)
	if branchID != "" {
		q = q.Where("branch_id = ? OR branch_id IS NULL", branchID)
	}
	var shift models.CashShift
	err := q.Order("opened_at DESC").First(&shift).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &shift, nil
}

// shiftCashSales suma las ventas EN EFECTIVO atadas al turno.
func shiftCashSales(db *gorm.DB, tenantID, shiftID string) (float64, int64, error) {
	var total float64
	var count int64
	row := db.Model(&models.Sale{}).
		Where("tenant_id = ? AND cash_shift_uuid = ?", tenantID, shiftID)
	if err := row.Count(&count).Error; err != nil {
		return 0, 0, err
	}
	err := db.Model(&models.Sale{}).
		Select("COALESCE(SUM(total),0)").
		Where("tenant_id = ? AND cash_shift_uuid = ? AND payment_method IN ?",
			tenantID, shiftID, cashInDrawer).
		Scan(&total).Error
	return total, count, err
}

// OpenCashShift — POST /cash-shifts {opening_amount, employee_uuid?, employee_name?}.
// 409 si ya hay un turno abierto en la sede (un cajón, un turno).
func OpenCashShift(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		OpeningAmount float64 `json:"opening_amount" binding:"min=0"`
		EmployeeUUID  string  `json:"employee_uuid"`
		EmployeeName  string  `json:"employee_name"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		branchID := middleware.GetBranchID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		existing, err := openShiftFor(db, tenantID, branchID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al consultar turnos"})
			return
		}
		if existing != nil {
			c.JSON(http.StatusConflict, gin.H{
				"error": "ya hay un turno de caja abierto",
				"code":  "shift_already_open",
				"data":  existing,
			})
			return
		}

		shift := models.CashShift{
			TenantID:      tenantID,
			BranchID:      middleware.UUIDPtr(branchID),
			OpenedBy:      middleware.UUIDPtr(req.EmployeeUUID),
			OpenedByName:  req.EmployeeName,
			Status:        models.CashShiftOpen,
			OpeningAmount: req.OpeningAmount,
			OpenedAt:      time.Now(),
		}
		if err := db.Create(&shift).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al abrir el turno"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": shift})
	}
}

// CurrentCashShift — GET /cash-shifts/current: turno abierto + esperado
// corriendo (base + efectivo atado hasta ahora). 404 si no hay abierto.
func CurrentCashShift(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		branchID := middleware.GetBranchID(c)

		shift, err := openShiftFor(db, tenantID, branchID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al consultar turnos"})
			return
		}
		if shift == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "no hay turno de caja abierto"})
			return
		}
		cash, salesCount, err := shiftCashSales(db, tenantID, shift.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al sumar las ventas"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"shift":            shift,
			"cash_sales":       cash,
			"sales_count":      salesCount,
			"running_expected": shift.OpeningAmount + cash,
		}})
	}
}

// CloseCashShift — POST /cash-shifts/:uuid/close {counted_amount, notes?}.
// Congela esperado = base + efectivo del turno y la diferencia
// (contado − esperado; negativo = faltante). Idempotente: cerrar un
// turno ya cerrado devuelve 409 con el resumen.
func CloseCashShift(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		CountedAmount *float64 `json:"counted_amount" binding:"required"`
		Notes         string   `json:"notes"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		uuid := c.Param("uuid")

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if *req.CountedAmount < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el conteo no puede ser negativo"})
			return
		}

		var shift models.CashShift
		if err := db.Where("id = ? AND tenant_id = ?", uuid, tenantID).
			First(&shift).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "turno no encontrado"})
			return
		}
		if shift.Status == models.CashShiftClosed {
			c.JSON(http.StatusConflict, gin.H{
				"error": "el turno ya estaba cerrado",
				"data":  shift,
			})
			return
		}

		cash, _, err := shiftCashSales(db, tenantID, shift.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al sumar las ventas"})
			return
		}
		now := time.Now()
		expected := shift.OpeningAmount + cash
		difference := *req.CountedAmount - expected

		updates := map[string]any{
			"status":          models.CashShiftClosed,
			"closed_at":       now,
			"closed_by":       middleware.UUIDPtr(userID),
			"counted_amount":  *req.CountedAmount,
			"expected_amount": expected,
			"difference":      difference,
			"notes":           req.Notes,
		}
		if err := db.Model(&shift).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al cerrar el turno"})
			return
		}
		db.First(&shift, "id = ?", shift.ID)
		c.JSON(http.StatusOK, gin.H{"data": shift})
	}
}

// ListCashShifts — GET /cash-shifts?limit=30: historial (más reciente
// primero) para el reporte del dueño. Solo back-office (el middleware
// bloquea waiter/chef/courier en main.go).
func ListCashShifts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)

		var shifts []models.CashShift
		if err := ApplyBranchScope(db.Where("tenant_id = ?", tenantID), scope).
			Order("opened_at DESC").Limit(60).
			Find(&shifts).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al listar turnos"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": shifts})
	}
}
