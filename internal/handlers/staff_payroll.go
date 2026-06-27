// Spec: specs/084-peluqueria-salon/spec.md
//
// API del motor de liquidación a profesionales (peluquería/barbería). La
// matemática vive en services/payroll_service.go (pura, testeada); aquí se
// consulta la BD, se prorratea IVA/propina por venta y se arma el resultado.
package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ── Config de pago por profesional ──────────────────────────────────────────

// GetEmployeePayConfig — GET /api/v1/employees/:id/pay-config
func GetEmployeePayConfig(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		empID := c.Param("uuid")
		var cfg models.EmployeePayConfig
		err := db.Where("tenant_id = ? AND employee_uuid = ? AND is_active = ?", tenantID, empID, true).
			Order("effective_from DESC").First(&cfg).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusOK, gin.H{"data": nil})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al cargar el esquema de pago"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": cfg})
	}
}

// UpsertEmployeePayConfig — PUT /api/v1/employees/:id/pay-config
// Effective-dated: desactiva la config activa previa y crea una nueva con
// effective_from=now. Conserva el historial (liquidaciones pasadas intactas).
func UpsertEmployeePayConfig(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		PayModel      string   `json:"pay_model" binding:"required"`
		BranchID      string   `json:"branch_id"`
		CommissionPct *float64 `json:"commission_pct"`
		FixedPerJob   *float64 `json:"fixed_per_job"`
		FixedUnit     string   `json:"fixed_unit"`
		BaseSalary    *float64 `json:"base_salary"`
		RentRate      *float64 `json:"rent_rate"`
		RentUnit      string   `json:"rent_unit"`
		WhoCollects   string   `json:"who_collects"`
		TipRate       *float64 `json:"tip_rate"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		empID := c.Param("uuid")
		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if _, ok := models.ValidPayModels[req.PayModel]; !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "modelo de pago no válido"})
			return
		}
		// Coherencia mínima: cada modelo exige su parámetro.
		switch req.PayModel {
		case models.PayModelCommission:
			if req.CommissionPct == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "indique el porcentaje de comisión"})
				return
			}
		case models.PayModelFixedPerJob:
			if req.FixedPerJob == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "indique el valor fijo por trabajo/turno"})
				return
			}
		case models.PayModelChairRent:
			if req.RentRate == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "indique el valor del arriendo"})
				return
			}
		case models.PayModelSalaryCommission:
			if req.BaseSalary == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "indique el sueldo base"})
				return
			}
		}

		fixedUnit := req.FixedUnit
		if fixedUnit == "" {
			fixedUnit = models.FixedUnitService
		}
		rentUnit := req.RentUnit
		if rentUnit == "" {
			rentUnit = models.RentUnitDaily
		}
		who := req.WhoCollects
		if who == "" {
			who = models.WhoCollectsShop
		}
		tip := 1.0
		if req.TipRate != nil {
			tip = *req.TipRate
		}

		cfg := models.EmployeePayConfig{
			TenantID:      tenantID,
			BranchID:      middleware.UUIDPtr(req.BranchID),
			EmployeeUUID:  empID,
			PayModel:      req.PayModel,
			CommissionPct: req.CommissionPct,
			FixedPerJob:   req.FixedPerJob,
			FixedUnit:     fixedUnit,
			BaseSalary:    req.BaseSalary,
			RentRate:      req.RentRate,
			RentUnit:      rentUnit,
			WhoCollects:   who,
			TipRate:       tip,
			EffectiveFrom: time.Now(),
			IsActive:      true,
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			// Desactivar la previa (el índice único parcial exige una sola activa).
			if err := tx.Model(&models.EmployeePayConfig{}).
				Where("tenant_id = ? AND employee_uuid = ? AND is_active = ?", tenantID, empID, true).
				Update("is_active", false).Error; err != nil {
				return err
			}
			return tx.Create(&cfg).Error
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar el esquema de pago", "detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": cfg})
	}
}

// ── Liquidación ─────────────────────────────────────────────────────────────

// LiquidationRow — liquidación de un profesional en el periodo (preview).
type LiquidationRow struct {
	EmployeeUUID string         `json:"employee_uuid"`
	EmployeeName string         `json:"employee_name"`
	PayModel     string         `json:"pay_model"`
	Payout       services.Payout `json:"payout"`
}

// parsePeriod lee from/until (yyyy-mm-dd o RFC3339). until es EXCLUSIVO. Default:
// últimos 30 días.
func parsePeriod(c *gin.Context) (time.Time, time.Time) {
	parse := func(s string) (time.Time, bool) {
		if s == "" {
			return time.Time{}, false
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t, true
		}
		return time.Time{}, false
	}
	now := time.Now()
	from, okF := parse(c.Query("from"))
	until, okU := parse(c.Query("until"))
	if !okU {
		until = now
	} else {
		// until inclusivo por día → sumar 24h si vino solo fecha.
		until = until.Add(24 * time.Hour)
	}
	if !okF {
		from = until.AddDate(0, 0, -30)
	}
	return from, until
}

// collectLiquidationLines arma las líneas de servicio atribuidas del periodo,
// prorrateando IVA y propina POR VENTA (largest-remainder), agrupadas por
// profesional. Reusa la atribución congelada (commission_amount) de cada línea.
func collectLiquidationLines(db *gorm.DB, tenantID string, scope BranchScopeResolution, from, until time.Time) map[string][]services.PayrollLine {
	var sales []models.Sale
	q := db.Preload("Items").
		Where("tenant_id = ? AND payment_status = ? AND created_at >= ? AND created_at < ?",
			tenantID, "COMPLETED", from, until)
	q = ApplyBranchScope(q, scope)
	q.Find(&sales)

	out := map[string][]services.PayrollLine{}
	for _, s := range sales {
		// Líneas de servicio de esta venta.
		var svc []models.SaleItem
		var grossWeights []float64
		for _, it := range s.Items {
			if it.IsService {
				svc = append(svc, it)
				grossWeights = append(grossWeights, it.Subtotal)
			}
		}
		if len(svc) == 0 {
			continue
		}
		// Prorrateo de IVA por gross; line_net = gross - line_tax.
		taxParts := services.ProrateLargestRemainder(s.TaxAmount, grossWeights)
		nets := make([]float64, len(svc))
		for i := range svc {
			nets[i] = svc[i].Subtotal - taxParts[i]
		}
		// Prorrateo de propina por net.
		tipParts := services.ProrateLargestRemainder(s.TipAmount, nets)
		day := s.CreatedAt.Format("2006-01-02")
		for i, it := range svc {
			emp := ""
			if it.EmployeeUUID != nil && *it.EmployeeUUID != "" {
				emp = *it.EmployeeUUID
			} else if s.EmployeeUUID != nil {
				emp = *s.EmployeeUUID
			}
			if emp == "" {
				emp = "_unassigned"
			}
			out[emp] = append(out[emp], services.PayrollLine{
				LineNet:          nets[i],
				PayBasis:         it.PayBasis,
				CommissionAmount: it.CommissionAmount,
				TipShare:         tipParts[i],
				SaleID:           s.ID,
				Day:              day,
			})
		}
	}
	return out
}

// employeeContext arma el PayrollContext desde la config activa del profesional.
// rentUnits = #días del periodo si daily, #semanas si weekly (aproximado).
func employeeContext(db *gorm.DB, tenantID, empID string, from, until time.Time) (services.PayrollContext, *models.EmployeePayConfig) {
	var cfg models.EmployeePayConfig
	err := db.Where("tenant_id = ? AND employee_uuid = ? AND is_active = ?", tenantID, empID, true).
		Order("effective_from DESC").First(&cfg).Error
	if err != nil {
		// Sin config → comisión 0 (solo lo congelado por línea, si hubiera).
		return services.PayrollContext{PayModel: models.PayModelCommission, TipRate: 1}, nil
	}
	days := until.Sub(from).Hours() / 24
	rentUnits := days
	if cfg.RentUnit == models.RentUnitWeekly {
		rentUnits = days / 7.0
	}
	ctx := services.PayrollContext{
		PayModel:    cfg.PayModel,
		FixedUnit:   cfg.FixedUnit,
		WhoCollects: cfg.WhoCollects,
		TipRate:     cfg.TipRate,
		RentUnits:   rentUnits,
	}
	if cfg.FixedPerJob != nil {
		ctx.FixedPerJob = *cfg.FixedPerJob
	}
	if cfg.BaseSalary != nil {
		ctx.BaseSalary = *cfg.BaseSalary
	}
	if cfg.RentRate != nil {
		ctx.RentRate = *cfg.RentRate
	}
	return ctx, &cfg
}

// GetLiquidation — GET /api/v1/payouts/liquidation?from=&until=&employee_uuid=
// Preview computado (no persiste): un row por profesional con el desglose.
func GetLiquidation(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)
		from, until := parsePeriod(c)
		filterEmp := strings.TrimSpace(c.Query("employee_uuid"))

		byEmp := collectLiquidationLines(db, tenantID, scope, from, until)

		// Nombres de empleados (snapshot de las líneas o tabla employees).
		names := map[string]string{}
		var emps []models.Employee
		db.Where("tenant_id = ?", tenantID).Find(&emps)
		for _, e := range emps {
			names[e.ID] = e.Name
		}

		rows := make([]LiquidationRow, 0, len(byEmp))
		for emp, lines := range byEmp {
			if emp == "_unassigned" {
				continue
			}
			if filterEmp != "" && emp != filterEmp {
				continue
			}
			ctx, cfg := employeeContext(db, tenantID, emp, from, until)
			payout := services.ComputePayout(lines, ctx)
			model := ctx.PayModel
			if cfg != nil {
				model = cfg.PayModel
			}
			rows = append(rows, LiquidationRow{
				EmployeeUUID: emp,
				EmployeeName: names[emp],
				PayModel:     model,
				Payout:       payout,
			})
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"from":   from.Format(time.RFC3339),
			"until":  until.Format(time.RFC3339),
			"rows":   rows,
		}})
	}
}

// ── Payouts (registro de pagos) ─────────────────────────────────────────────

// CreatePayout — POST /api/v1/payouts (admin/owner). Persiste una liquidación/
// anticipo/arriendo con su desglose snapshot. Append-only.
func CreatePayout(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var p models.EmployeePayout
		if err := c.ShouldBindJSON(&p); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if p.EmployeeUUID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "empleado requerido"})
			return
		}
		p.TenantID = tenantID
		p.BranchID = middleware.GetBranchIDPtr(c)
		if p.Status == "" {
			p.Status = models.PayoutStatusPaid
		}
		if p.Kind == "" {
			p.Kind = models.PayoutKindLiquidacion
		}
		if p.Direction == "" {
			p.Direction = models.PayoutDirectionToPro
		}
		now := time.Now()
		if p.PaidAt == nil {
			p.PaidAt = &now
		}
		if err := db.Create(&p).Error; err != nil {
			// Idempotencia: liquidación duplicada del mismo periodo (índice único).
			if isRetryableConflict(err) {
				c.JSON(http.StatusConflict, gin.H{"error": "ya existe una liquidación pagada para este profesional y periodo"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo registrar el pago", "detail": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": p})
	}
}

// ListPayouts — GET /api/v1/payouts?employee_uuid=&from=&until=&status=
func ListPayouts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		q := db.Where("tenant_id = ?", tenantID)
		if e := strings.TrimSpace(c.Query("employee_uuid")); e != "" {
			q = q.Where("employee_uuid = ?", e)
		}
		if s := strings.TrimSpace(c.Query("status")); s != "" {
			q = q.Where("status = ?", s)
		}
		var rows []models.EmployeePayout
		q.Order("period_start DESC, created_at DESC").Limit(200).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"data": rows})
	}
}

// VoidPayout — POST /api/v1/payouts/:id/void (admin/owner). Anula sin borrar.
func VoidPayout(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		id := c.Param("id")
		res := db.Model(&models.EmployeePayout{}).
			Where("id = ? AND tenant_id = ?", id, tenantID).
			Update("status", models.PayoutStatusVoid)
		if res.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo anular"})
			return
		}
		if res.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "pago no encontrado"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "pago anulado"})
	}
}
