package handlers

import (
	"net/http"
	"strings"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func AnalyticsDashboard(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)

		startOfToday := startOfTenantDay(tenantNow())

		var totalSales float64
		var transactionCount int64
		ApplyBranchScope(db.Model(&models.Sale{}), scope).
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, startOfToday).
			Count(&transactionCount).
			Select("COALESCE(SUM(total), 0)").
			Scan(&totalSales)

		var totalCredit float64
		ApplyBranchScope(db.Model(&models.CreditAccount{}), scope).
			Where("tenant_id = ? AND status IN ('open', 'partial')", tenantID).
			Select("COALESCE(SUM(total_amount - paid_amount), 0)").
			Scan(&totalCredit)

		var productCount int64
		ApplyBranchScope(db.Model(&models.Product{}), scope).
			Where("tenant_id = ? AND is_available = true", tenantID).
			Count(&productCount)

		var lowStockCount int64
		ApplyBranchScope(db.Model(&models.Product{}), scope).
			Where("tenant_id = ? AND is_available = true AND stock <= min_stock AND min_stock > 0", tenantID).
			Count(&lowStockCount)

		var pendingOrders int64
		ApplyBranchScope(db.Model(&models.OrderTicket{}), scope).
			Where("tenant_id = ? AND status IN ('nuevo', 'preparando')", tenantID).
			Count(&pendingOrders)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"total_sales_today":    totalSales,
				"transaction_count":    transactionCount,
				"total_credit_pending": totalCredit,
				"product_count":        productCount,
				"low_stock_count":      lowStockCount,
				"pending_orders":       pendingOrders,
			},
		})
	}
}

// FinancialSummary returns the full financial cube the dashboard
// needs to support owner/manager decisions:
//   - headline (total + count + avg ticket + vs-previous %)
//   - cash flow buckets (cash drawer / digital / accounts receivable)
//   - by_method, by_channel, by_hour, by_weekday slices
//   - peak_hour + first_sale_at to spot opening-hour patterns
//   - top_employees ranked by sales total + estimated profit
//   - best_day / worst_day for week/month windows
//
// Filters (query params, all optional):
//
//	period=today|week|month       — coarse window (default today)
//	since=ISO8601 & until=ISO8601 — overrides period for custom range
//	employee=<name>               — narrow to a single employee
//	source=POS|WEB|TABLE          — narrow to a single channel
//	payment_method=cash|transfer  — narrow to a single method
//
// GET /api/v1/analytics/financial-summary
func FinancialSummary(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		period := c.DefaultQuery("period", "today")

		now := tenantNow()
		since, prevSince, prevEnd := windowFor(period, now,
			c.Query("since"), c.Query("until"))

		empFilter := strings.TrimSpace(c.Query("employee"))
		sourceFilter := strings.TrimSpace(c.Query("source"))
		methodFilter := strings.TrimSpace(c.Query("payment_method"))

		// Single base query that every aggregation builds on. Wrapping
		// in a closure keeps the WHERE clauses identical across slices
		// — cheaper than re-typing the same predicates for each query
		// and means a new filter only needs to be added once.
		scope := ResolveBranchScope(c, db)
		baseSales := func() *gorm.DB {
			q := ApplyBranchScope(db.Model(&models.Sale{}), scope).
				Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
				Where("created_at >= ?", since)
			// `until` is implied by the period upper bound for today
			// (= now), or by the explicit `until` query param via
			// windowFor. We never include cancelled / soft-deleted.
			if untilStr := c.Query("until"); untilStr != "" {
				if t, err := time.Parse(time.RFC3339, untilStr); err == nil {
					q = q.Where("created_at <= ?", t)
				}
			}
			if empFilter != "" {
				q = q.Where("employee_name = ?", empFilter)
			}
			if sourceFilter != "" {
				q = q.Where("source = ?", sourceFilter)
			}
			if methodFilter != "" {
				q = q.Where("payment_method = ?", methodFilter)
			}
			return q
		}

		// ── by_method ──
		type MethodTotal struct {
			PaymentMethod string  `json:"payment_method"`
			Total         float64 `json:"total"`
			Count         int64   `json:"count"`
		}
		var byMethod []MethodTotal
		baseSales().
			Select("payment_method, COALESCE(SUM(total), 0) as total, COUNT(*) as count").
			Group("payment_method").
			Scan(&byMethod)

		var totalSales float64
		var totalCount int64
		cashInDrawer := 0.0
		digitalMoney := 0.0
		creditTotal := 0.0
		for _, m := range byMethod {
			totalSales += m.Total
			totalCount += m.Count
			switch m.PaymentMethod {
			case "cash":
				cashInDrawer = m.Total
			case "transfer", "card", "nequi", "daviplata":
				digitalMoney += m.Total
			case "credit":
				creditTotal = m.Total
			}
		}
		avgTicket := 0.0
		if totalCount > 0 {
			avgTicket = totalSales / float64(totalCount)
		}

		// ── by_channel (source) ──
		type ChannelTotal struct {
			Source string  `json:"source"`
			Total  float64 `json:"total"`
			Count  int64   `json:"count"`
		}
		var byChannel []ChannelTotal
		baseSales().
			Select("source, COALESCE(SUM(total), 0) as total, COUNT(*) as count").
			Group("source").
			Scan(&byChannel)

		// ── by_hour (0-23) ──
		type HourTotal struct {
			Hour  int     `json:"hour"`
			Total float64 `json:"total"`
			Count int64   `json:"count"`
		}
		var byHour []HourTotal
		baseSales().
			Select("EXTRACT(HOUR FROM created_at)::int AS hour, " +
				"COALESCE(SUM(total), 0) as total, COUNT(*) as count").
			Group("hour").
			Order("hour").
			Scan(&byHour)

		// Derive peak hour + share %.
		var peakHour *struct {
			Hour     int     `json:"hour"`
			Total    float64 `json:"total"`
			SharePct float64 `json:"share_pct"`
		}
		for _, h := range byHour {
			if peakHour == nil || h.Total > peakHour.Total {
				peakHour = &struct {
					Hour     int     `json:"hour"`
					Total    float64 `json:"total"`
					SharePct float64 `json:"share_pct"`
				}{Hour: h.Hour, Total: h.Total}
			}
		}
		if peakHour != nil && totalSales > 0 {
			peakHour.SharePct = (peakHour.Total / totalSales) * 100
		}

		// ── by_weekday (0=Sun..6=Sat) — only meaningful when window > 1 day ──
		type WeekdayTotal struct {
			Weekday int     `json:"weekday"`
			Name    string  `json:"name"`
			Total   float64 `json:"total"`
			Count   int64   `json:"count"`
		}
		var byWeekday []WeekdayTotal
		baseSales().
			Select("EXTRACT(DOW FROM created_at)::int AS weekday, " +
				"COALESCE(SUM(total), 0) as total, COUNT(*) as count").
			Group("weekday").
			Order("weekday").
			Scan(&byWeekday)
		for i := range byWeekday {
			byWeekday[i].Name = weekdayLabel(byWeekday[i].Weekday)
		}

		var bestDay, worstDay *WeekdayTotal
		for i := range byWeekday {
			w := byWeekday[i]
			if bestDay == nil || w.Total > bestDay.Total {
				bestDay = &w
			}
			if worstDay == nil || (w.Count > 0 && w.Total < worstDay.Total) {
				worstDay = &w
			}
		}

		// ── first_sale_at ──
		var firstSale *time.Time
		baseSales().
			Select("MIN(created_at)").
			Scan(&firstSale)

		// ── top_employees (with profit estimate) ──
		// LEFT JOIN sale_items + products so the cost is per-line and
		// services / non-product items don't break the aggregation.
		type EmployeeRow struct {
			Name       string  `json:"name"`
			SalesTotal float64 `json:"sales_total"`
			TxCount    int64   `json:"tx_count"`
			Profit     float64 `json:"profit"`
		}
		var topEmployees []EmployeeRow
		empQuery := db.Table("sales s").
			Select(`COALESCE(NULLIF(s.employee_name, ''), 'Sin asignar') AS name,
				COALESCE(SUM(s.total), 0)                       AS sales_total,
				COUNT(*)                                        AS tx_count,
				COALESCE(SUM(s.total - COALESCE(line_cost.cost, 0)), 0) AS profit`).
			Joins(`LEFT JOIN (
				SELECT sale_id, SUM(quantity * COALESCE(p.purchase_price, 0)) AS cost
				FROM sale_items
				LEFT JOIN products p ON p.id = sale_items.product_id
				GROUP BY sale_id
			) line_cost ON line_cost.sale_id = s.id`).
			Where("s.tenant_id = ? AND s.deleted_at IS NULL", tenantID).
			Where("s.created_at >= ?", since).
			Group("name").
			Order("sales_total DESC").
			Limit(10)
		if empFilter != "" {
			empQuery = empQuery.Where("s.employee_name = ?", empFilter)
		}
		if sourceFilter != "" {
			empQuery = empQuery.Where("s.source = ?", sourceFilter)
		}
		if methodFilter != "" {
			empQuery = empQuery.Where("s.payment_method = ?", methodFilter)
		}
		if untilStr := c.Query("until"); untilStr != "" {
			if t, err := time.Parse(time.RFC3339, untilStr); err == nil {
				empQuery = empQuery.Where("s.created_at <= ?", t)
			}
		}
		// Multi-sede: el ranking debe ser de la SEDE filtrada, no de todas.
		if scope.BranchID != "" {
			empQuery = empQuery.Where("s.branch_id = ? OR s.branch_id IS NULL", scope.BranchID)
		}
		empQuery.Scan(&topEmployees)

		// ── total profit (window-wide) ──
		var totalCost float64
		qCost := db.Model(&models.SaleItem{}).
			Select("COALESCE(SUM(sale_items.quantity * p.purchase_price), 0)").
			Joins("JOIN sales ON sales.id = sale_items.sale_id").
			Joins("JOIN products p ON p.id = sale_items.product_id").
			Where("sales.tenant_id = ? AND sales.deleted_at IS NULL", tenantID).
			Where("sales.created_at >= ?", since)
		// Multi-sede: el costo (y la ganancia) debe ser de la SEDE filtrada.
		if scope.BranchID != "" {
			qCost = qCost.Where("sales.branch_id = ? OR sales.branch_id IS NULL", scope.BranchID)
		}
		qCost.Scan(&totalCost)
		// Non-product costs booked directly on the sale (event per-attendee cost,
		// Source="EVENT"). Mirrors baseSales so scope/filters apply consistently.
		var totalCostAmount float64
		baseSales().Select("COALESCE(SUM(cost_amount), 0)").Scan(&totalCostAmount)
		totalProfit := totalSales - totalCost - totalCostAmount

		// ── accounts receivable (always tenant-wide) ──
		var accountsReceivable float64
		db.Model(&models.CreditAccount{}).
			Where("tenant_id = ? AND status IN ('open', 'partial')", tenantID).
			Select("COALESCE(SUM(total_amount - paid_amount), 0)").
			Scan(&accountsReceivable)

		// ── 30-day daily average (always tenant-wide) ──
		thirtyDaysAgo := now.AddDate(0, 0, -30)
		var last30Total float64
		db.Model(&models.Sale{}).
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, thirtyDaysAgo).
			Select("COALESCE(SUM(total), 0)").
			Scan(&last30Total)
		dailyAvg := last30Total / 30

		// ── vs previous period (% change) ──
		var prevTotal float64
		// Multi-sede: comparar la MISMA sede contra su propio período anterior.
		ApplyBranchScope(db.Model(&models.Sale{}), scope).
			Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
			Where("created_at >= ? AND created_at < ?", prevSince, prevEnd).
			Select("COALESCE(SUM(total), 0)").
			Scan(&prevTotal)
		var vsPrevPct *float64
		if prevTotal > 0 {
			pct := (totalSales - prevTotal) / prevTotal * 100
			vsPrevPct = &pct
		}

		// ── available employees + sources for the filter UI ──
		var employees []string
		// Multi-sede: el desplegable muestra empleados de la SEDE filtrada.
		ApplyBranchScope(db.Model(&models.Sale{}), scope).
			Where("tenant_id = ? AND employee_name <> ''", tenantID).
			Distinct("employee_name").
			Order("employee_name").
			Pluck("employee_name", &employees)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"period":              period,
				"since":               since,
				"total_sales":         totalSales,
				"transaction_count":   totalCount,
				"avg_ticket":          avgTicket,
				"total_profit":        totalProfit,
				"daily_average":       dailyAvg,
				"vs_previous_pct":     vsPrevPct,
				"cash_in_drawer":      cashInDrawer,
				"digital_money":       digitalMoney,
				"credit_paid_total":   creditTotal,
				"accounts_receivable": accountsReceivable,
				"by_method":           byMethod,
				"by_channel":          byChannel,
				"by_hour":             byHour,
				"by_weekday":          byWeekday,
				"best_day":            bestDay,
				"worst_day":           worstDay,
				"first_sale_at":       firstSale,
				"peak_hour":           peakHour,
				"top_employees":       topEmployees,
				"available_employees": employees,
				"filters": gin.H{
					"employee":       empFilter,
					"source":         sourceFilter,
					"payment_method": methodFilter,
				},
			},
		})
	}
}

// windowFor resolves (since, prevSince, prevEnd) for a given period.
// `prevSince`/`prevEnd` describe the same-length window immediately
// before `since`, used for vs-previous comparison.
func windowFor(period string, now time.Time, sinceQ, untilQ string) (time.Time, time.Time, time.Time) {
	if sinceQ != "" {
		if t, err := time.Parse(time.RFC3339, sinceQ); err == nil {
			until := now
			if untilQ != "" {
				if u, err := time.Parse(time.RFC3339, untilQ); err == nil {
					until = u
				}
			}
			win := until.Sub(t)
			return t, t.Add(-win), t
		}
	}
	switch period {
	case "week":
		since := now.AddDate(0, 0, -7)
		return since, since.AddDate(0, 0, -7), since
	case "month":
		since := now.AddDate(0, -1, 0)
		return since, since.AddDate(0, -1, 0), since
	default: // today
		since := startOfTenantDay(now)
		return since, since.AddDate(0, 0, -1), since
	}
}

func weekdayLabel(dow int) string {
	switch dow {
	case 0:
		return "Domingo"
	case 1:
		return "Lunes"
	case 2:
		return "Martes"
	case 3:
		return "Miércoles"
	case 4:
		return "Jueves"
	case 5:
		return "Viernes"
	case 6:
		return "Sábado"
	}
	return ""
}

// SalesHistoryByPeriod returns paginated sales filtered by period.
// GET /api/v1/analytics/sales-history?period=today&page=1&per_page=20
func SalesHistoryByPeriod(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)
		period := c.DefaultQuery("period", "today")
		p := parsePagination(c)

		now := tenantNow()
		var since time.Time
		switch period {
		case "week":
			since = now.AddDate(0, 0, -7)
		case "month":
			since = now.AddDate(0, -1, 0)
		default:
			since = startOfTenantDay(now)
		}

		var total int64
		ApplyBranchScope(db.Model(&models.Sale{}), scope).
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, since).
			Count(&total)

		var sales []models.Sale
		ApplyBranchScope(db.Preload("Items"), scope).
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, since).
			Order("created_at DESC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&sales)

		c.JSON(http.StatusOK, newPaginatedResponse(sales, total, p))
	}
}

// ProductInsights consolidates the "what to act on" view for the
// dashboard's product card: top sellers, slow movers, and items
// near expiry. Single endpoint, single round-trip — the screen can
// paint everything with one fetch.
//
// GET /api/v1/analytics/products-insights?period=30d
func ProductInsights(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)
		period := c.DefaultQuery("period", "30d")

		var since time.Time
		switch period {
		case "7d":
			since = time.Now().AddDate(0, 0, -7)
		case "30d":
			since = time.Now().AddDate(0, 0, -30)
		case "90d":
			since = time.Now().AddDate(0, 0, -90)
		default:
			since = time.Now().AddDate(0, 0, -30)
		}

		// ── top_sellers — most quantity moved in the window ──
		type TopSeller struct {
			ProductID string  `json:"product_id"`
			Name      string  `json:"name"`
			Quantity  int     `json:"quantity"`
			Revenue   float64 `json:"revenue"`
			ImageURL  string  `json:"image_url"`
			Stock     int     `json:"stock"`
		}
		var topSellers []TopSeller
		qTop := db.Table("sale_items si").
			Select(`si.product_id,
				MAX(si.name) AS name,
				SUM(si.quantity) AS quantity,
				SUM(si.subtotal) AS revenue,
				MAX(COALESCE(p.image_url, '')) AS image_url,
				MAX(COALESCE(p.stock, 0)) AS stock`).
			Joins("JOIN sales s ON s.id = si.sale_id").
			Joins("LEFT JOIN products p ON p.id = si.product_id").
			Where("s.tenant_id = ? AND s.created_at >= ? AND s.deleted_at IS NULL",
				tenantID, since).
			// product_id es UUID en Postgres: `<> ''` lanza
			// "invalid input syntax for type uuid" en runtime → la query
			// fallaba y top_sellers volvía VACÍO (el módulo mostraba "aún
			// no hay ventas" aunque sí las había). Un UUID es NULL o
			// válido; basta con IS NOT NULL. (sqlite lo toleraba en tests.)
			Where("si.product_id IS NOT NULL")
		if scope.BranchID != "" {
			qTop = qTop.Where("s.branch_id = ?", scope.BranchID)
		}
		qTop.Group("si.product_id").
			Order("quantity DESC").
			Limit(10).
			Scan(&topSellers)

		// ── slow_movers — available products that sold < 3 units in
		// the window (or never sold). LEFT JOIN keeps zero-sale
		// products in the result. We exclude AGOTADO (stock 0) so the
		// list focuses on stuff sitting on the shelf.
		type SlowMover struct {
			ProductID  string  `json:"product_id"`
			Name       string  `json:"name"`
			Stock      int     `json:"stock"`
			Price      float64 `json:"price"`
			ImageURL   string  `json:"image_url"`
			Quantity   int     `json:"quantity_sold"`
			LastSaleAt *string `json:"last_sale_at,omitempty"`
		}
		var slowMovers []SlowMover
		qSlow := db.Table("products p").
			Select(`p.id AS product_id,
				p.name,
				p.stock,
				p.price,
				COALESCE(p.image_url, '') AS image_url,
				COALESCE(SUM(si.quantity), 0) AS quantity,
				MAX(s.created_at)::text AS last_sale_at`).
			Joins(`LEFT JOIN sale_items si ON si.product_id = p.id`).
			Joins(`LEFT JOIN sales s ON s.id = si.sale_id
				AND s.tenant_id = p.tenant_id
				AND s.deleted_at IS NULL
				AND s.created_at >= ?`, since).
			Where("p.tenant_id = ? AND p.is_available = true AND p.deleted_at IS NULL AND p.stock > 0",
				tenantID)
		if scope.BranchID != "" {
			qSlow = qSlow.Where("p.branch_id = ? OR p.branch_id IS NULL", scope.BranchID)
		}
		qSlow.Group("p.id, p.name, p.stock, p.price, p.image_url").
			Having("COALESCE(SUM(si.quantity), 0) < 3").
			Order("quantity ASC, p.updated_at ASC").
			Limit(15).
			Scan(&slowMovers)

		// ── expiring_soon — items with expiry_date in the next
		// 30 days (or already past, so the merchant can pull them).
		type Expiring struct {
			ProductID     string  `json:"product_id"`
			Name          string  `json:"name"`
			Stock         int     `json:"stock"`
			Price         float64 `json:"price"`
			PurchasePrice float64 `json:"purchase_price"`
			ImageURL      string  `json:"image_url"`
			ExpiryDate    string  `json:"expiry_date"`
			DaysLeft      int     `json:"days_left"`
		}
		var expiring []Expiring
		thirtyDaysAhead := time.Now().AddDate(0, 0, 30).Format("2006-01-02")
		qExp := db.Table("products p").
			Select(`p.id AS product_id, p.name, p.stock, p.price,
				COALESCE(p.purchase_price, 0) AS purchase_price,
				COALESCE(p.image_url, '') AS image_url,
				p.expiry_date::text AS expiry_date,
				EXTRACT(DAY FROM (p.expiry_date::timestamp - NOW()))::int AS days_left`).
			Where("p.tenant_id = ? AND p.is_available = true AND p.deleted_at IS NULL AND p.stock > 0",
				tenantID).
			Where("p.expiry_date IS NOT NULL AND p.expiry_date <= ?", thirtyDaysAhead)
		if scope.BranchID != "" {
			qExp = qExp.Where("p.branch_id = ? OR p.branch_id IS NULL", scope.BranchID)
		}
		qExp.Order("p.expiry_date ASC").
			Limit(20).
			Scan(&expiring)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"period":         period,
				"top_sellers":    topSellers,
				"slow_movers":    slowMovers,
				"expiring_soon":  expiring,
				"no_sales_count": len(slowMovers),
				"expiring_count": len(expiring),
			},
		})
	}
}

func TopProducts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)
		period := c.DefaultQuery("period", "7d")

		var since time.Time
		switch period {
		case "1d":
			since = time.Now().AddDate(0, 0, -1)
		case "7d":
			since = time.Now().AddDate(0, 0, -7)
		case "30d":
			since = time.Now().AddDate(0, 0, -30)
		default:
			since = time.Now().AddDate(0, 0, -7)
		}

		type TopProduct struct {
			ProductID string  `json:"product_id"`
			Name      string  `json:"name"`
			Quantity  int     `json:"quantity"`
			Revenue   float64 `json:"revenue"`
		}

		var top []TopProduct
		q := db.Model(&models.SaleItem{}).
			Select("sale_items.product_id, sale_items.name, SUM(sale_items.quantity) as quantity, SUM(sale_items.subtotal) as revenue").
			Joins("JOIN sales ON sales.id = sale_items.sale_id").
			Where("sales.tenant_id = ? AND sales.created_at >= ? AND sales.deleted_at IS NULL", tenantID, since)
		if scope.BranchID != "" {
			q = q.Where("sales.branch_id = ?", scope.BranchID)
		}
		q.Group("sale_items.product_id, sale_items.name").
			Order("quantity DESC").
			Limit(20).
			Scan(&top)

		c.JSON(http.StatusOK, gin.H{"data": top})
	}
}

func PhotoCoverage(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)

		var totalProducts int64
		ApplyBranchScope(db.Model(&models.Product{}), scope).
			Where("tenant_id = ? AND is_available = true", tenantID).
			Count(&totalProducts)

		var withPhoto int64
		ApplyBranchScope(db.Model(&models.Product{}), scope).
			Where("tenant_id = ? AND is_available = true AND (photo_url != '' OR image_url != '')", tenantID).
			Count(&withPhoto)

		percentage := float64(0)
		if totalProducts > 0 {
			percentage = float64(withPhoto) / float64(totalProducts) * 100
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"total_products": totalProducts,
				"with_photo":     withPhoto,
				"without_photo":  totalProducts - withPhoto,
				"percentage":     percentage,
			},
		})
	}
}

func SalesByEmployee(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)

		startOfToday := startOfTenantDay(tenantNow())

		type EmployeeSales struct {
			EmployeeUUID string  `json:"employee_uuid"`
			EmployeeName string  `json:"employee_name"`
			SaleCount    int64   `json:"sale_count"`
			TotalAmount  float64 `json:"total_amount"`
		}

		// FR-01 — `employee_uuid` is a Postgres `uuid` column; comparing
		// it against the empty string ('') raises 22P02 (invalid input
		// syntax for type uuid). GORM silences that error on Scan, so the
		// endpoint returned `null` for every tenant. Filter unattributed
		// sales out with `IS NOT NULL`, which is the type-correct way to
		// drop rows that never carried an employee.
		result := []EmployeeSales{}
		ApplyBranchScope(db.Model(&models.Sale{}), scope).
			Select("employee_uuid, employee_name, COUNT(*) as sale_count, SUM(total) as total_amount").
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL AND employee_uuid IS NOT NULL",
				tenantID, startOfToday).
			Group("employee_uuid, employee_name").
			Order("total_amount DESC").
			Scan(&result)

		c.JSON(http.StatusOK, gin.H{"data": result})
	}
}

func InventoryHealth(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)

		// Una sola pasada a products (antes 4): SUM + COUNT(*) FILTER. stock>0 se
		// quita de la base (aporta 0 al SUM) para que los FILTER de low/out vean
		// todas las filas; los números son idénticos. db.Model conserva el
		// deleted_at IS NULL y el branch scope. Audit 2026-06-24.
		var agg struct {
			Cost   float64
			Retail float64
			Low    int64
			Out    int64
		}
		ApplyBranchScope(db.Model(&models.Product{}), scope).
			Where("tenant_id = ? AND is_available = true", tenantID).
			Select(`COALESCE(SUM(purchase_price * stock), 0) AS cost,
				COALESCE(SUM(price * stock), 0) AS retail,
				COUNT(*) FILTER (WHERE stock <= min_stock AND min_stock > 0) AS low,
				COUNT(*) FILTER (WHERE stock = 0) AS out`).
			Scan(&agg)
		totalValue := agg.Cost
		totalRetailValue := agg.Retail
		lowStockCount := agg.Low
		outOfStockCount := agg.Out

		sevenDays := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
		var expiringCount int64
		ApplyBranchScope(db.Model(&models.Product{}), scope).
			Where("tenant_id = ? AND is_available = true AND expiry_date IS NOT NULL AND expiry_date <= ?", tenantID, sevenDays).
			Count(&expiringCount)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"inventory_cost_value":   totalValue,
				"inventory_retail_value": totalRetailValue,
				"potential_profit":       totalRetailValue - totalValue,
				"low_stock_count":        lowStockCount,
				"out_of_stock_count":     outOfStockCount,
				"expiring_count":         expiringCount,
			},
		})
	}
}

func IngestionMethod(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)

		type MethodCount struct {
			Method string `json:"method"`
			Count  int64  `json:"count"`
		}

		var result []MethodCount
		ApplyBranchScope(db.Model(&models.Product{}), scope).
			Select("ingestion_method as method, COUNT(*) as count").
			Where("tenant_id = ? AND is_available = true", tenantID).
			Group("ingestion_method").
			Scan(&result)

		c.JSON(http.StatusOK, gin.H{"data": result})
	}
}
