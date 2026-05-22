// Spec: specs/033-difusion-promociones/spec.md
package services

import (
	"fmt"
	"math"
	"sort"
	"time"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// RFM audience filter identifiers (Spec F033 §4 "Selector de audiencia").
const (
	AudienceFilterAll      = "all"
	AudienceFilterFrequent = "frequent"
	AudienceFilterVIP      = "vip"
	AudienceFilterDormant  = "dormant"
	AudienceFilterRecent   = "recent"
)

// Window constants for the RFM segmentation.
const (
	audienceFrequentDays  = 30 // "Frecuentes": >=3 compras en 30d
	audienceFrequentMin   = 3  // minimum purchases inside the window
	audienceDormantDays   = 30 // "Dormidos": sin compras en 30d
	audienceRecentDays    = 7  // "Recientes": compra en 7d
	audienceVIPPercentile = 0.20
)

// AudienceCustomer is one row of a segmentation result: the customer's
// identity plus the three RFM aggregates computed from sales. It is the
// payload the audience endpoint hands the Flutter selector.
//
// LastPurchaseAt is nil for a customer with zero sales. Name is PII and
// is only ever returned through the JWT-protected audience endpoint —
// never through a public route.
type AudienceCustomer struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Phone          string  `json:"phone"`
	TotalSpent     float64 `json:"total_spent"`
	PurchaseCount  int64   `json:"purchase_count"`
	LastPurchaseAt *string `json:"last_purchase_at"`
	// FrequentCount is the count of purchases inside the 30-day
	// frequency window; exposed so the UI can explain why a customer
	// matched "Frecuentes".
	FrequentCount int64 `json:"frequent_count"`
}

// audienceRow is the raw GORM scan target. It mirrors AudienceCustomer
// but receives the aggregate columns directly from the SELECT.
type audienceRow struct {
	ID             string
	Name           string
	Phone          string
	TotalSpent     float64
	PurchaseCount  int64
	LastPurchaseAt *string
	FrequentCount  int64
}

// BuildAudience returns the list of customers in `tenantID` that match
// the requested RFM `filter`, every one of them carrying a non-empty
// phone (a customer without a phone cannot be reached by WhatsApp, so
// it is excluded from every segment — Spec F033 R4).
//
// `now` is injected so the 7/30-day windows are deterministic in tests.
// The aggregates are computed in a single LEFT JOIN to sales scoped to
// the tenant; anonymous sales never enter because the join is on
// customer_id. The query uses LOWER/MAX/COUNT/SUM only — no
// Postgres-only syntax — so the same code runs on the SQLite test
// driver and on production Postgres.
func BuildAudience(db *gorm.DB, tenantID, filter string, now time.Time) ([]AudienceCustomer, error) {
	switch filter {
	case AudienceFilterAll, AudienceFilterFrequent, AudienceFilterVIP,
		AudienceFilterDormant, AudienceFilterRecent:
	default:
		return nil, fmt.Errorf("filtro de audiencia inválido: %q", filter)
	}

	frequentSince := now.AddDate(0, 0, -audienceFrequentDays)
	dormantBefore := now.AddDate(0, 0, -audienceDormantDays)
	recentSince := now.AddDate(0, 0, -audienceRecentDays)

	// Base: tenant customers with a non-empty phone. The aggregates are
	// produced by a LEFT JOIN so a customer with zero sales still
	// appears (count 0, total 0, last NULL) — required by the Dormant
	// filter which must catch never-bought customers.
	var rows []audienceRow
	err := db.Model(&models.Customer{}).
		Select(`customers.id AS id,
			customers.name AS name,
			customers.phone AS phone,
			COALESCE(SUM(sales.total), 0) AS total_spent,
			COUNT(sales.id) AS purchase_count,
			MAX(sales.created_at) AS last_purchase_at,
			COUNT(CASE WHEN sales.created_at >= ? THEN 1 END) AS frequent_count`,
			frequentSince).
		Joins(`LEFT JOIN sales ON sales.customer_id = customers.id
			AND sales.tenant_id = customers.tenant_id
			AND sales.deleted_at IS NULL`).
		Where("customers.tenant_id = ? AND customers.phone <> '' AND customers.deleted_at IS NULL", tenantID).
		Group("customers.id").
		Order("customers.name ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("error al construir audiencia: %w", err)
	}

	switch filter {
	case AudienceFilterAll:
		return toAudience(rows), nil

	case AudienceFilterFrequent:
		return toAudience(filterRows(rows, func(r audienceRow) bool {
			return r.FrequentCount >= audienceFrequentMin
		})), nil

	case AudienceFilterRecent:
		return toAudience(filterRows(rows, func(r audienceRow) bool {
			return r.LastPurchaseAt != nil &&
				parsedAfter(*r.LastPurchaseAt, recentSince)
		})), nil

	case AudienceFilterDormant:
		return toAudience(filterRows(rows, func(r audienceRow) bool {
			// No purchases at all, or last purchase older than 30 days.
			if r.PurchaseCount == 0 || r.LastPurchaseAt == nil {
				return true
			}
			return !parsedAfter(*r.LastPurchaseAt, dormantBefore)
		})), nil

	case AudienceFilterVIP:
		return toAudience(topSpenders(rows, audienceVIPPercentile)), nil
	}

	return nil, fmt.Errorf("filtro de audiencia inválido: %q", filter)
}

// filterRows keeps the rows for which keep() is true, preserving order.
func filterRows(rows []audienceRow, keep func(audienceRow) bool) []audienceRow {
	out := make([]audienceRow, 0, len(rows))
	for _, r := range rows {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

// topSpenders returns the top `pct` fraction of rows by total_spent.
// The cut size is ceil(pct*N) so even a tiny customer base yields at
// least one VIP whenever there is at least one customer. Computed in Go
// (sort + slice) instead of a SQL window function so the same code path
// works on the SQLite test driver. Tenants are small (<5k customers,
// plan D1), so the in-memory sort is negligible.
func topSpenders(rows []audienceRow, pct float64) []audienceRow {
	if len(rows) == 0 {
		return nil
	}
	sorted := make([]audienceRow, len(rows))
	copy(sorted, rows)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].TotalSpent > sorted[j].TotalSpent
	})
	cut := int(math.Ceil(pct * float64(len(sorted))))
	if cut < 1 {
		cut = 1
	}
	if cut > len(sorted) {
		cut = len(sorted)
	}
	return sorted[:cut]
}

// parsedAfter reports whether the timestamp string `ts` (as stored by
// the driver in created_at) is at or after `cutoff`. SQLite and
// Postgres serialise timestamps differently, so we try the common
// layouts and fall back to "not after" on an unparseable value — a
// conservative default that simply omits an ambiguous row.
func parsedAfter(ts string, cutoff time.Time) bool {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, l := range layouts {
		if parsed, err := time.Parse(l, ts); err == nil {
			return !parsed.Before(cutoff)
		}
	}
	return false
}

// toAudience maps the raw scan rows into the public payload shape.
func toAudience(rows []audienceRow) []AudienceCustomer {
	out := make([]AudienceCustomer, 0, len(rows))
	for _, r := range rows {
		out = append(out, AudienceCustomer{
			ID:             r.ID,
			Name:           r.Name,
			Phone:          r.Phone,
			TotalSpent:     r.TotalSpent,
			PurchaseCount:  r.PurchaseCount,
			LastPurchaseAt: r.LastPurchaseAt,
			FrequentCount:  r.FrequentCount,
		})
	}
	return out
}
