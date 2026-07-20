// Spec: specs/107-dashboard-v2-resumen/spec.md
//
// GET /api/v1/dashboard/summary — la ÚNICA llamada del inicio v2 (FR-01).
// Agrega lo que hoy calculan varios endpoints REUSANDO sus mismos predicados
// (Art. VII: una cifra = una fórmula):
//   - ventas hoy      = AnalyticsDashboard (analytics_tenant.go)
//   - ganancia hoy    = FinancialSummary (misma fórmula COGS + cost_amount)
//   - fiados          = SUM(total-paid) accepted open/partial, por sede
//   - turno de caja   = openShiftFor (cash_shifts.go)
//   - en curso        = OpenOrderStatuses + online pending/accepted
//   - stock bajo      = stock <= min_stock AND min_stock > 0 (dashboard)
//   - tareas          = los mismos agregadores de ListTasks SIN ranking IA
package handlers

import (
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
)

const summaryMovements = 5

type summaryMovement struct {
	Kind     string    `json:"kind"` // sale | credit_payment | online_order
	Title    string    `json:"title"`
	Subtitle string    `json:"subtitle"`
	Amount   int64     `json:"amount"`
	Sign     int       `json:"sign"` // +1 entra, -1 fía/sale
	Status   string    `json:"status"`
	At       time.Time `json:"at"`
}

func DashboardSummary(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)
		if scope.NotOwned {
			c.JSON(http.StatusForbidden, gin.H{"error": "esa sede no pertenece a su negocio"})
			return
		}
		startOfToday := startOfTenantDay(tenantNow())

		// ── ventas de hoy (misma fórmula que AnalyticsDashboard) ──
		var salesTotal float64
		var salesCount int64
		ApplyBranchScope(db.Model(&models.Sale{}), scope).
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, startOfToday).
			Count(&salesCount).
			Select("COALESCE(SUM(total), 0)").
			Scan(&salesTotal)

		// ── ganancia de hoy (misma fórmula que FinancialSummary) ──
		var cogs float64
		qCost := db.Model(&models.SaleItem{}).
			Select("COALESCE(SUM(sale_items.quantity * p.purchase_price), 0)").
			Joins("JOIN sales ON sales.id = sale_items.sale_id").
			Joins("JOIN products p ON p.id = sale_items.product_id").
			Where("sales.tenant_id = ? AND sales.deleted_at IS NULL", tenantID).
			Where("sales.created_at >= ?", startOfToday)
		if scope.BranchID != "" {
			qCost = qCost.Where("sales.branch_id = ? OR sales.branch_id IS NULL", scope.BranchID)
		}
		qCost.Scan(&cogs)
		var costAmount float64
		ApplyBranchScope(db.Model(&models.Sale{}), scope).
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, startOfToday).
			Select("COALESCE(SUM(cost_amount), 0)").
			Scan(&costAmount)
		profit := salesTotal - cogs - costAmount
		marginPct := 0.0
		if salesTotal > 0 {
			marginPct = profit / salesTotal * 100
		}

		// ── turno de caja (Spec 105) ──
		shift, _ := openShiftFor(db, tenantID, scope.BranchID)
		shiftOut := gin.H{"open": false}
		if shift != nil {
			shiftOut = gin.H{
				"open":      true,
				"opened_at": shift.OpenedAt,
				"opened_by": shift.OpenedByName,
			}
		}

		// ── cuentas por cobrar (fiados accepted con saldo, por sede) ──
		var receivablesTotal int64
		var debtors int64
		var oldest *time.Time
		recvQ := ApplyBranchScope(db.Model(&models.CreditAccount{}), scope).
			Where("tenant_id = ? AND status IN ('open','partial') AND fiado_status = 'accepted'", tenantID)
		recvQ.Session(&gorm.Session{}).
			Select("COALESCE(SUM(total_amount - paid_amount), 0)").Scan(&receivablesTotal)
		recvQ.Session(&gorm.Session{}).
			Distinct("customer_id").Count(&debtors)
		recvQ.Session(&gorm.Session{}).
			Select("MIN(created_at)").Scan(&oldest)
		oldestDays := 0
		if oldest != nil {
			oldestDays = int(time.Since(*oldest).Hours() / 24)
		}

		// ── en curso ──
		var tables int64
		ApplyBranchScope(db.Model(&models.OrderTicket{}), scope).
			Where("tenant_id = ? AND status IN ? AND paid_at IS NULL",
				tenantID, models.OpenOrderStatuses()).
			Count(&tables)
		var kitchen int64
		ApplyBranchScope(db.Model(&models.OrderTicket{}), scope).
			Where("tenant_id = ? AND status IN ('nuevo','preparando')", tenantID).
			Count(&kitchen)
		// Pedidos online: tenant-wide a propósito (consistencia con Tareas 078).
		var online int64
		db.Model(&models.OnlineOrder{}).
			Where("tenant_id = ? AND status IN ('pending','accepted')", tenantID).
			Count(&online)

		// ── stock bajo (mismo predicado del dashboard actual) ──
		var lowStockCount int64
		lowQ := ApplyBranchScope(db.Model(&models.Product{}), scope).
			Where("tenant_id = ? AND is_available = true AND stock <= min_stock AND min_stock > 0", tenantID)
		lowQ.Session(&gorm.Session{}).Count(&lowStockCount)
		var examples []string
		lowQ.Session(&gorm.Session{}).Order("stock ASC").Limit(3).
			Pluck("name", &examples)

		// ── movimientos recientes (ventas + abonos + pedidos online de hoy) ──
		movements := collectMovements(db, scope, tenantID, startOfToday)

		// ── tareas: mismos agregadores de ListTasks, sin ranking IA ──
		urgent, actionable := summaryTaskCounts(db, tenantID, scope.BranchID)

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"sales_today": gin.H{"total": int64(salesTotal), "count": salesCount},
			"profit_today": gin.H{
				"amount": int64(profit), "margin_pct": int(marginPct + 0.5),
			},
			"cash_shift":  shiftOut,
			"receivables": gin.H{"total": receivablesTotal, "debtors": debtors, "oldest_days": oldestDays},
			"in_progress": gin.H{"tables": tables, "kitchen": kitchen, "online": online},
			"low_stock":   gin.H{"count": lowStockCount, "examples": examples},
			"movements":   movements,
			"tasks":       gin.H{"urgent": urgent, "actionable": actionable},
			"generated_at": time.Now().UTC(),
		}})
	}
}

// collectMovements une las últimas ventas, abonos de fiado y pedidos online
// del día y devuelve los summaryMovements más recientes.
func collectMovements(db *gorm.DB, scope BranchScopeResolution, tenantID string, since time.Time) []summaryMovement {
	out := make([]summaryMovement, 0, summaryMovements*2)

	var sales []models.Sale
	ApplyBranchScope(db.Model(&models.Sale{}), scope).
		Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, since).
		Order("created_at DESC").Limit(summaryMovements).
		Find(&sales)
	for _, s := range sales {
		out = append(out, summaryMovement{
			Kind:     "sale",
			Title:    "Venta — " + paymentLabel(string(s.PaymentMethod)),
			Subtitle: "",
			Amount:   int64(s.Total),
			Sign:     +1,
			Status:   "Pagada",
			At:       s.CreatedAt,
		})
	}

	// Abonos de fiado: CreditPayment no tiene tenant_id — JOIN con la cuenta.
	type payRow struct {
		Amount    int64
		CreatedAt time.Time
	}
	var pays []payRow
	db.Table("credit_payments").
		Select("credit_payments.amount, credit_payments.created_at").
		Joins("JOIN credit_accounts ON credit_accounts.id = credit_payments.credit_account_id").
		Where("credit_accounts.tenant_id = ? AND credit_payments.created_at >= ? AND credit_payments.deleted_at IS NULL",
			tenantID, since).
		Order("credit_payments.created_at DESC").Limit(summaryMovements).
		Scan(&pays)
	for _, p := range pays {
		out = append(out, summaryMovement{
			Kind:   "credit_payment",
			Title:  "Abono de fiado",
			Amount: p.Amount,
			Sign:   +1,
			Status: "Abonado",
			At:     p.CreatedAt,
		})
	}

	var orders []models.OnlineOrder
	db.Model(&models.OnlineOrder{}).
		Where("tenant_id = ? AND created_at >= ?", tenantID, since).
		Order("created_at DESC").Limit(summaryMovements).
		Find(&orders)
	for _, o := range orders {
		out = append(out, summaryMovement{
			Kind:     "online_order",
			Title:    "Pedido en línea — " + o.CustomerName,
			Subtitle: o.Status,
			Amount:   int64(o.TotalAmount),
			Sign:     +1,
			Status:   onlineStatusLabel(o.Status),
			At:       o.CreatedAt,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > summaryMovements {
		out = out[:summaryMovements]
	}
	return out
}

// summaryTaskCounts reusa los agregadores de ListTasks (Spec 078) sin el
// re-ranking por IA — para un contador, el orden no importa.
func summaryTaskCounts(db *gorm.DB, tenantID, branchID string) (urgent, actionable int) {
	tasks := make([]models.Task, 0, 16)
	tasks = append(tasks, onlineOrderTasks(db, tenantID)...)
	tasks = append(tasks, tableAccountTasks(db, tenantID, branchID)...)
	tasks = append(tasks, errandTasks(db, tenantID)...)
	if t, ok := outOfStockTask(db, tenantID, branchID); ok {
		tasks = append(tasks, t)
	}
	if t, ok := reorderTask(db, tenantID, branchID); ok {
		tasks = append(tasks, t)
	}
	if t, ok := perishableTask(db, tenantID, branchID); ok {
		tasks = append(tasks, t)
	}
	if t, ok := incompleteMenuTask(db, tenantID); ok {
		tasks = append(tasks, t)
	}
	tasks = filterDismissed(db, tenantID, tasks)
	tasks = dedupeAndSort(tasks)
	for _, t := range tasks {
		switch t.Urgency {
		case models.TaskUrgencyCritical, models.TaskUrgencyHigh:
			urgent++
			actionable++
		case models.TaskUrgencyNormal:
			actionable++
		}
	}
	return urgent, actionable
}

func paymentLabel(method string) string {
	switch method {
	case "cash", "efectivo":
		return "efectivo"
	case "":
		return "sin método"
	default:
		return method
	}
}

func onlineStatusLabel(status string) string {
	switch status {
	case "pending":
		return "Por confirmar"
	case "accepted":
		return "Aceptado"
	case "rejected":
		return "Rechazado"
	case "completed":
		return "Entregado"
	default:
		return status
	}
}
