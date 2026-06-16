package handlers

import (
	"net/http"
	"time"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GodModeTenantRow is the consolidated shape returned by
// GET /api/v1/admin/tenants. One query per list (tenants +
// subscription join) plus two cheap GROUP BY lookups for branches /
// employees counts — keep this under 4 round-trips so the dashboard
// stays fast even at 10k tenants.
type GodModeTenantRow struct {
	ID                   string     `json:"id"`
	BusinessName         string     `json:"business_name"`
	BusinessType         string     `json:"business_type"`
	BusinessTypes        []string   `json:"business_types"`
	OwnerName            string     `json:"owner_name"`
	Phone                string     `json:"phone"`
	Location             string     `json:"location"`
	Address              string     `json:"address"`
	BranchesCount        int        `json:"branches_count"`
	EmployeesCount       int        `json:"employees_count"`
	SubscriptionStatus   string     `json:"subscription_status"`
	TrialEndsAt          *time.Time `json:"trial_ends_at"`
	TrialDaysRemaining   int        `json:"trial_days_remaining"`
	IsPremium            bool       `json:"is_premium"`
	CreatedAt            time.Time  `json:"created_at"`
	LastSyncAt           *time.Time `json:"last_sync_at"`
	PendingSyncOps       int        `json:"pending_sync_ops"`
	// Actividad del tenant (god-mode móvil): cuántas referencias tiene,
	// cuántos módulos activos, cómo crea los productos y qué ventas lleva.
	ProductCount       int            `json:"product_count"`
	ActiveModulesCount int            `json:"active_modules_count"`
	SalesCount         int64          `json:"sales_count"`
	SalesTotal         float64        `json:"sales_total"`
	IngestionBreakdown map[string]int `json:"ingestion_breakdown"`
	// H18 fix: the `legacy_subscription_status` /
	// `legacy_subscription_ends_at` columns used to be exposed here
	// from `Tenant.SubscriptionStatus` / `Tenant.SubscriptionEndsAt`
	// as a parallel-but-stale source of truth. The dashboard never
	// used them; removing them eliminates the dual-source risk
	// flagged in the audit. The single source is now `subs[t.ID]`
	// (TenantSubscription rows), which feeds `SubscriptionStatus`
	// above and `IsPremium` via `IsPremium(now)`.
}

// ProductStats agrega, por tenant, cuántas referencias tiene y cómo las
// crea (ingestion_method → conteo).
type ProductStats struct {
	Count    int
	ByMethod map[string]int
}

// SalesStats agrega ventas por tenant (cantidad + total).
type SalesStats struct {
	Count int64
	Total float64
}

// BuildGodModeTenants is the pure function that transforms the raw
// data (tenants + subscriptions + counts + activity) into the response
// shape. Exported + stateless so it's unit-testable without a DB — the
// handler wraps it with the actual queries.
func BuildGodModeTenants(
	tenants []models.Tenant,
	subs map[string]models.TenantSubscription,
	branchCounts map[string]int,
	employeeCounts map[string]int,
	productStats map[string]ProductStats,
	salesStats map[string]SalesStats,
	now time.Time,
) []GodModeTenantRow {
	out := make([]GodModeTenantRow, 0, len(tenants))
	for _, t := range tenants {
		sub := subs[t.ID]
		subPtr := &sub
		// Zero struct (no row) ≠ premium; surface as FREE-equivalent so
		// the dashboard badge renders meaningfully instead of blank.
		subStatus := sub.Status
		if subStatus == "" {
			subStatus = models.SubscriptionStatusFree
			subPtr = nil
		}

		primaryType := ""
		if len(t.BusinessTypes) > 0 {
			primaryType = t.BusinessTypes[0]
		}

		pstat := productStats[t.ID]
		breakdown := pstat.ByMethod
		if breakdown == nil {
			breakdown = map[string]int{}
		}
		sstat := salesStats[t.ID]

		out = append(out, GodModeTenantRow{
			ID:                 t.ID,
			BusinessName:       t.BusinessName,
			BusinessType:       primaryType,
			BusinessTypes:      t.BusinessTypes,
			OwnerName:          t.OwnerName,
			Phone:              t.Phone,
			Location:           t.Address,
			Address:            t.Address,
			BranchesCount:      branchCounts[t.ID],
			EmployeesCount:     employeeCounts[t.ID],
			SubscriptionStatus: subStatus,
			TrialEndsAt:        sub.TrialEndsAt,
			TrialDaysRemaining: subPtr.TrialDaysRemaining(now),
			IsPremium:          subPtr.IsPremium(now),
			CreatedAt:          t.CreatedAt,
			LastSyncAt:         t.LastSyncAt,
			PendingSyncOps:     t.PendingSyncOps,
			ProductCount:       pstat.Count,
			ActiveModulesCount: t.CountActiveModules(),
			SalesCount:         sstat.Count,
			SalesTotal:         sstat.Total,
			IngestionBreakdown: breakdown,
		})
	}
	return out
}

// AdminListTenants is the god-mode endpoint. Returns the consolidated
// shape super admins need to monitor every tenant: business, location,
// subscription state, trial days remaining, branches / employees
// counts. The response is wrapped in the same `{ "data": [...] }`
// envelope the admin-web SWR layer expects.
func AdminListTenants(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := time.Now()

		var tenants []models.Tenant
		if err := db.Select(
			// `subscription_status` / `subscription_ends_at` legacy columns
			// no longer hidrated — H18 fix. Subscription state comes from
			// `tenant_subscriptions` via `loadSubscriptionsByTenantID` below.
			// Las columnas enable_* + feature_flags hidratan para contar
			// módulos activos por tenant (CountActiveModules).
			"id, created_at, owner_name, phone, business_name, business_types, "+
				"address, last_sync_at, pending_sync_ops, feature_flags, "+
				"enable_price_tiers, enable_customer_management, enable_quotes, "+
				"enable_promotions, enable_marketing_hub, enable_recipes, "+
				"enable_supplies, enable_furniture_jobs, enable_purchase_orders",
		).Order("created_at DESC").Find(&tenants).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener tenants"})
			return
		}

		ids := make([]string, 0, len(tenants))
		for _, t := range tenants {
			ids = append(ids, t.ID)
		}

		subs := loadSubscriptionsByTenantID(db, ids)
		branchCounts := loadCountByTenantID(db, &models.Branch{}, ids)
		employeeCounts := loadCountByTenantID(db, &models.Employee{}, ids)
		productStats := loadProductStatsByTenantID(db, ids)
		salesStats := loadSalesStatsByTenantID(db, ids)

		rows := BuildGodModeTenants(
			tenants, subs, branchCounts, employeeCounts,
			productStats, salesStats, now)
		c.JSON(http.StatusOK, gin.H{"data": rows})
	}
}

// loadProductStatsByTenantID devuelve, por tenant, el total de productos
// y el desglose por ingestion_method (manual / import / ia_factura /
// barcode_scan). Una sola query agrupada — sin N+1.
func loadProductStatsByTenantID(db *gorm.DB, ids []string) map[string]ProductStats {
	out := make(map[string]ProductStats, len(ids))
	if len(ids) == 0 {
		return out
	}
	type row struct {
		TenantID        string
		IngestionMethod string
		Count           int
	}
	var rows []row
	db.Model(&models.Product{}).
		Select("tenant_id, ingestion_method, COUNT(*) AS count").
		Where("tenant_id IN ?", ids).
		Group("tenant_id, ingestion_method").
		Scan(&rows)
	for _, r := range rows {
		ps := out[r.TenantID]
		if ps.ByMethod == nil {
			ps.ByMethod = map[string]int{}
		}
		method := r.IngestionMethod
		if method == "" {
			method = "manual"
		}
		ps.ByMethod[method] += r.Count
		ps.Count += r.Count
		out[r.TenantID] = ps
	}
	return out
}

// loadSalesStatsByTenantID devuelve, por tenant, la cantidad de ventas y
// el total acumulado. Una sola query agrupada.
func loadSalesStatsByTenantID(db *gorm.DB, ids []string) map[string]SalesStats {
	out := make(map[string]SalesStats, len(ids))
	if len(ids) == 0 {
		return out
	}
	type row struct {
		TenantID string
		Count    int64
		Total    float64
	}
	var rows []row
	db.Model(&models.Sale{}).
		Select("tenant_id, COUNT(*) AS count, COALESCE(SUM(total), 0) AS total").
		Where("tenant_id IN ?", ids).
		Group("tenant_id").
		Scan(&rows)
	for _, r := range rows {
		out[r.TenantID] = SalesStats{Count: r.Count, Total: r.Total}
	}
	return out
}

func loadSubscriptionsByTenantID(db *gorm.DB, ids []string) map[string]models.TenantSubscription {
	out := make(map[string]models.TenantSubscription, len(ids))
	if len(ids) == 0 {
		return out
	}
	var rows []models.TenantSubscription
	if err := db.Where("tenant_id IN ?", ids).Find(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		out[r.TenantID] = r
	}
	return out
}

// loadCountByTenantID runs a single grouped query against any model
// that has a tenant_id column and returns a count per tenant. Avoids
// the N+1 that a per-tenant Count() would produce.
func loadCountByTenantID(db *gorm.DB, model any, ids []string) map[string]int {
	out := make(map[string]int, len(ids))
	if len(ids) == 0 {
		return out
	}
	type row struct {
		TenantID string
		Count    int
	}
	var rows []row
	db.Model(model).
		Select("tenant_id, COUNT(*) AS count").
		Where("tenant_id IN ?", ids).
		Group("tenant_id").
		Scan(&rows)
	for _, r := range rows {
		out[r.TenantID] = r.Count
	}
	return out
}
