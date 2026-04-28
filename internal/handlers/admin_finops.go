package handlers

import (
	"net/http"
	"time"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ── GET /api/v1/admin/analytics/ai-costs ─────────────────────────────

type aiCostDayPoint struct {
	Date         string  `json:"date"`
	CostUSD      float64 `json:"cost_usd"`
	TokensInput  int64   `json:"tokens_input"`
	TokensOutput int64   `json:"tokens_output"`
}

type aiFeatureCost struct {
	Feature string  `json:"feature"`
	CostUSD float64 `json:"cost_usd"`
}

type topTenantCost struct {
	TenantID     string  `json:"tenant_id"`
	BusinessName string  `json:"business_name"`
	CostUSD      float64 `json:"cost_usd"`
}

type finopsMetaCosts struct {
	// AiLogsInPeriod is rows matching the requested date filter — if 0,
	// either no Gemini traffic was logged yet or usageMetadata was empty.
	AiLogsInPeriod int64 `json:"ai_logs_in_period"`
	AiLogsTotal    int64 `json:"ai_logs_total"`
}

type adminAICostsResponse struct {
	From        string            `json:"from"`
	To          string            `json:"to"`
	PeriodTotal float64           `json:"period_total_usd"`
	Daily       []aiCostDayPoint  `json:"daily"`
	ByFeature   []aiFeatureCost   `json:"by_feature"`
	TopTenants  []topTenantCost   `json:"top_tenants"`
	Meta        finopsMetaCosts   `json:"meta"`
}

func AdminAICosts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		from, to, ok := parseFinopsDateRange(c)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Indique from=YYYY-MM-DD y to=YYYY-MM-DD, u omita ambos para los últimos 30 días."})
			return
		}
		// `to` query is inclusive calendar day; convert to exclusive end.
		endExclusive := to.AddDate(0, 0, 1)

		var periodTotal float64
		db.Model(&models.AIUsageLog{}).
			Where("created_at >= ? AND created_at < ?", from, endExclusive).
			Select("COALESCE(SUM(estimated_cost_usd),0)").Scan(&periodTotal)

		var daily []struct {
			Day         string  `gorm:"column:day"`
			CostUSD     float64 `gorm:"column:cost_usd"`
			TokensIn    int64   `gorm:"column:tokens_in"`
			TokensOut   int64   `gorm:"column:tokens_out"`
		}
		db.Raw(`
			SELECT to_char((created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day,
				COALESCE(SUM(estimated_cost_usd), 0) AS cost_usd,
				COALESCE(SUM(tokens_input), 0) AS tokens_in,
				COALESCE(SUM(tokens_output), 0) AS tokens_out
			FROM ai_usage_logs
			WHERE created_at >= ? AND created_at < ?
			GROUP BY 1
			ORDER BY 1 ASC
		`, from, endExclusive).Scan(&daily)

		dailyOut := make([]aiCostDayPoint, 0, len(daily))
		for _, r := range daily {
			dailyOut = append(dailyOut, aiCostDayPoint{
				Date:         r.Day,
				CostUSD:      r.CostUSD,
				TokensInput:  r.TokensIn,
				TokensOutput: r.TokensOut,
			})
		}

		var aiLogsInPeriod int64
		db.Model(&models.AIUsageLog{}).
			Where("created_at >= ? AND created_at < ?", from, endExclusive).
			Count(&aiLogsInPeriod)
		var aiLogsTotal int64
		db.Model(&models.AIUsageLog{}).Count(&aiLogsTotal)

		var byFeat []struct {
			Feature string
			Cost    float64
		}
		db.Raw(`
			SELECT feature, COALESCE(SUM(estimated_cost_usd), 0) AS cost
			FROM ai_usage_logs
			WHERE created_at >= ? AND created_at < ?
			GROUP BY feature
			ORDER BY cost DESC
		`, from, endExclusive).Scan(&byFeat)
		bf := make([]aiFeatureCost, 0, len(byFeat))
		for _, x := range byFeat {
			bf = append(bf, aiFeatureCost{Feature: x.Feature, CostUSD: x.Cost})
		}

		var top []struct {
			TenantID     string
			BusinessName string
			Cost         float64
		}
		db.Raw(`
			SELECT l.tenant_id, t.business_name, COALESCE(SUM(l.estimated_cost_usd), 0) AS cost
			FROM ai_usage_logs l
			JOIN tenants t ON t.id = l.tenant_id
			WHERE l.created_at >= ? AND l.created_at < ?
			GROUP BY l.tenant_id, t.business_name
			ORDER BY cost DESC
			LIMIT 15
		`, from, endExclusive).Scan(&top)
		topOut := make([]topTenantCost, 0, len(top))
		for _, t := range top {
			topOut = append(topOut, topTenantCost{
				TenantID: t.TenantID, BusinessName: t.BusinessName, CostUSD: t.Cost,
			})
		}

		c.JSON(http.StatusOK, adminAICostsResponse{
			From:        from.Format("2006-01-02"),
			To:          to.Format("2006-01-02"),
			PeriodTotal: periodTotal,
			Daily:       dailyOut,
			ByFeature:   bf,
			TopTenants:  topOut,
			Meta: finopsMetaCosts{
				AiLogsInPeriod: aiLogsInPeriod,
				AiLogsTotal:    aiLogsTotal,
			},
		})
	}
}

// ── GET /api/v1/admin/analytics/revenue ──────────────────────────────

type revenueDayPoint struct {
	Date      string  `json:"date"`
	RevenueUSD float64 `json:"revenue_usd"`
}

type finopsMetaRevenue struct {
	PaymentsConfirmedInPeriod int64 `json:"payments_confirmed_in_period"`
	PaymentsConfirmedTotal    int64 `json:"payments_confirmed_total"`
}

type adminRevenueResponse struct {
	From        string            `json:"from"`
	To          string            `json:"to"`
	PeriodTotal float64           `json:"period_total_usd"`
	Daily       []revenueDayPoint `json:"daily"`
	Meta        finopsMetaRevenue `json:"meta"`
}

func AdminSubscriptionRevenue(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		from, to, ok := parseFinopsDateRange(c)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid from/to (YYYY-MM-DD)"})
			return
		}
		endExclusive := to.AddDate(0, 0, 1)

		var total float64
		db.Model(&models.SubscriptionPayment{}).
			Where("status = ? AND COALESCE(confirmed_at, created_at) >= ? AND COALESCE(confirmed_at, created_at) < ?",
				models.SubscriptionPaymentStatusConfirmed, from, endExclusive).
			Select("COALESCE(SUM(amount_usd),0)").Scan(&total)

		var daily []struct {
			Day   string  `gorm:"column:day"`
			Total float64 `gorm:"column:total"`
		}
		db.Raw(`
			SELECT to_char((COALESCE(confirmed_at, created_at) AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day,
				COALESCE(SUM(amount_usd), 0) AS total
			FROM subscription_payments
			WHERE status = ? AND COALESCE(confirmed_at, created_at) >= ? AND COALESCE(confirmed_at, created_at) < ?
			GROUP BY 1
			ORDER BY 1 ASC
		`, models.SubscriptionPaymentStatusConfirmed, from, endExclusive).Scan(&daily)

		dOut := make([]revenueDayPoint, 0, len(daily))
		for _, r := range daily {
			dOut = append(dOut, revenueDayPoint{
				Date:       r.Day,
				RevenueUSD: r.Total,
			})
		}

		var payInPeriod int64
		db.Model(&models.SubscriptionPayment{}).
			Where("status = ? AND COALESCE(confirmed_at, created_at) >= ? AND COALESCE(confirmed_at, created_at) < ?",
				models.SubscriptionPaymentStatusConfirmed, from, endExclusive).
			Count(&payInPeriod)
		var payTotal int64
		db.Model(&models.SubscriptionPayment{}).
			Where("status = ?", models.SubscriptionPaymentStatusConfirmed).
			Count(&payTotal)

		c.JSON(http.StatusOK, adminRevenueResponse{
			From:        from.Format("2006-01-02"),
			To:          to.Format("2006-01-02"),
			PeriodTotal: total,
			Daily:       dOut,
			Meta: finopsMetaRevenue{
				PaymentsConfirmedInPeriod: payInPeriod,
				PaymentsConfirmedTotal:    payTotal,
			},
		})
	}
}

// ── GET /api/v1/admin/analytics/profitability ────────────────────────

type adminProfitabilityResponse struct {
	Month              string  `json:"month"`
	ProMonthlyPriceUSD float64 `json:"pro_monthly_price_usd"`
	RevenueUSD         float64 `json:"revenue_usd"`
	AICostUSD          float64 `json:"ai_cost_usd"`
	NetContributionUSD float64 `json:"net_contribution_usd"`
	ContributionMargin float64 `json:"contribution_margin_pct"`
	ProSubscribers     int64   `json:"pro_subscribers"`
	CostPerProUserUSD  float64 `json:"ai_cost_per_pro_user_usd"`
	MarginAtRisk       bool    `json:"margin_at_risk"`
	Meta               struct {
		AiLogsInMonth int64 `json:"ai_logs_in_month"`
		PaymentsInMonth int64 `json:"payments_confirmed_in_month"`
	} `json:"meta"`
}

// AdminProfitability aggregates subscription revenue vs AI spend for a
// calendar month (UTC). Query: month=YYYY-MM (default: current month).
func AdminProfitability(db *gorm.DB, proMonthlyListUSD float64) gin.HandlerFunc {
	if proMonthlyListUSD <= 0 {
		proMonthlyListUSD = 29.99
	}
	return func(c *gin.Context) {
		month := c.Query("month")
		if month == "" {
			month = time.Now().UTC().Format("2006-01")
		}
		start, end, ok := monthBoundsUTC(month)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "month must be YYYY-MM"})
			return
		}

		var revenue float64
		db.Model(&models.SubscriptionPayment{}).
			Where("status = ? AND COALESCE(confirmed_at, created_at) >= ? AND COALESCE(confirmed_at, created_at) < ?",
				models.SubscriptionPaymentStatusConfirmed, start, end).
			Select("COALESCE(SUM(amount_usd),0)").Scan(&revenue)

		var aiCost float64
		db.Model(&models.AIUsageLog{}).
			Where("created_at >= ? AND created_at < ?", start, end).
			Select("COALESCE(SUM(estimated_cost_usd),0)").Scan(&aiCost)

		var proCount int64
		db.Model(&models.TenantSubscription{}).
			Where("status = ?", models.SubscriptionStatusProActive).
			Count(&proCount)

		net := revenue - aiCost
		var marginPct float64
		if revenue > 0 {
			marginPct = 100.0 * net / revenue
		}

		var costPerPro float64
		if proCount > 0 {
			costPerPro = aiCost / float64(proCount)
		}
		atRisk := proCount > 0 && costPerPro >= 0.5*proMonthlyListUSD

		var aiLogsMonth int64
		db.Model(&models.AIUsageLog{}).
			Where("created_at >= ? AND created_at < ?", start, end).
			Count(&aiLogsMonth)
		var payMonth int64
		db.Model(&models.SubscriptionPayment{}).
			Where("status = ? AND COALESCE(confirmed_at, created_at) >= ? AND COALESCE(confirmed_at, created_at) < ?",
				models.SubscriptionPaymentStatusConfirmed, start, end).
			Count(&payMonth)

		resp := adminProfitabilityResponse{
			Month:              month,
			ProMonthlyPriceUSD: proMonthlyListUSD,
			RevenueUSD:         revenue,
			AICostUSD:          aiCost,
			NetContributionUSD: net,
			ContributionMargin: marginPct,
			ProSubscribers:     proCount,
			CostPerProUserUSD:  costPerPro,
			MarginAtRisk:       atRisk,
		}
		resp.Meta.AiLogsInMonth = aiLogsMonth
		resp.Meta.PaymentsInMonth = payMonth

		c.JSON(http.StatusOK, resp)
	}
}

func parseFinopsDateRange(c *gin.Context) (from, to time.Time, ok bool) {
	fs, ts := c.Query("from"), c.Query("to")
	now := time.Now().UTC()
	if fs == "" && ts == "" {
		to = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		from = to.AddDate(0, 0, -29) // 30 days inclusive
		return from, to, true
	}
	if fs == "" || ts == "" {
		return time.Time{}, time.Time{}, false
	}
	f, e1 := time.Parse("2006-01-02", fs)
	t, e2 := time.Parse("2006-01-02", ts)
	if e1 != nil || e2 != nil {
		return time.Time{}, time.Time{}, false
	}
	return f, t, true
}

func monthBoundsUTC(ym string) (start, end time.Time, ok bool) {
	t, err := time.Parse("2006-01", ym)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	end = start.AddDate(0, 1, 0)
	return start, end, true
}
