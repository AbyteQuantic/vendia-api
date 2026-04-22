package handlers

import (
	"net/http"
	"strings"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ── Types ────────────────────────────────────────────────────────────────────

// CrossIdentityParticipation describes one (tenant, role) pair a
// shared-identity user appears in. Consumed by the admin UI to render
// the cross-identity table row-by-row.
type CrossIdentityParticipation struct {
	TenantID           string `json:"tenant_id"`
	BusinessName       string `json:"business_name"`
	Role               string `json:"role"`
	SubscriptionStatus string `json:"subscription_status"`
	IsPremium          bool   `json:"is_premium"`
}

// CrossIdentityRecord is one user who shows up in multiple workspaces.
// The phone is masked before it leaves the server — PII minimisation
// matters especially for support dashboards that can have broader
// access than the actual tenants.
type CrossIdentityRecord struct {
	UserID            string                       `json:"user_id"`
	PhoneMasked       string                       `json:"phone_masked"`
	WorkspaceCount    int                          `json:"workspace_count"`
	Participations    []CrossIdentityParticipation `json:"participations"`
	// EvasionAlert flags the suspicious pattern the brief calls out:
	// an owner of a FREE/TRIAL tenant who also holds a non-owner role
	// in a PRO tenant (ie: the real business is run on PRO but the
	// owner's personal tenant sits in FREE to dodge the bill).
	EvasionAlert  bool   `json:"evasion_alert"`
	EvasionReason string `json:"evasion_reason,omitempty"`
}

// EcosystemMetrics is the response for GET /api/v1/admin/ecosystem/metrics.
// Integer COP cents are fine for the debt total — the Flutter client
// already formats with 0 decimals.
type EcosystemMetrics struct {
	FiadoOpenCount      int64 `json:"fiado_open_count"`
	FiadoClosedCount    int64 `json:"fiado_closed_count"`
	OnlineOrdersCount   int64 `json:"online_orders_count"`
	OutstandingDebtCOP  int64 `json:"outstanding_debt_cop"`
}

// ── Phone masking ────────────────────────────────────────────────────────────

// MaskPhone collapses the middle digits of an international number so
// the dashboard surfaces enough to confirm identity in ops chat without
// exposing a full phone. Examples:
//   "+573001234567" → "+57 300 *** 4567"
//   "3001234567"    → "300 *** 4567"
//   ""              → ""
//
// Kept exported because the frontend could call it too, but mainly so
// the test package can assert on deterministic output without depending
// on runtime formatting quirks.
func MaskPhone(phone string) string {
	trimmed := strings.TrimSpace(phone)
	if trimmed == "" {
		return ""
	}

	hasPlus := strings.HasPrefix(trimmed, "+")
	digits := make([]rune, 0, len(trimmed))
	for _, r := range trimmed {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
		}
	}
	n := len(digits)
	if n < 7 {
		// Too short to mask meaningfully — return as-is rather than
		// an awkward "*** ***" that hides nothing useful.
		return trimmed
	}

	last4 := string(digits[n-4:])
	switch {
	case hasPlus && n >= 12:
		// "+57 300 *** 4567" — Colombia mobile-shaped numbers.
		return "+" + string(digits[:2]) + " " + string(digits[2:5]) + " *** " + last4
	case n >= 10:
		// Domestic 10-digit (Colombian mobile without country code).
		return string(digits[:3]) + " *** " + last4
	default:
		return "*** " + last4
	}
}

// ── Cross-identity query ─────────────────────────────────────────────────────

// CrossIdentityRow matches the shape of the JOIN that pulls every
// (user, tenant, role) record where the user participates in 2+
// tenants. GORM scans into this intermediate so the pure-function
// BuildCrossIdentityRecords can re-use the data without re-querying.
type CrossIdentityRow struct {
	UserID             string
	Phone              string
	TenantID           string
	BusinessName       string
	Role               string
	SubscriptionStatus string
}

// BuildCrossIdentityRecords groups flat rows by user and computes
// masked phones + evasion alerts. Exported so the tests can verify
// the grouping and alert logic without touching the DB.
func BuildCrossIdentityRecords(rows []CrossIdentityRow) []CrossIdentityRecord {
	byUser := make(map[string]*CrossIdentityRecord)
	order := make([]string, 0)

	for _, r := range rows {
		rec, ok := byUser[r.UserID]
		if !ok {
			rec = &CrossIdentityRecord{
				UserID:      r.UserID,
				PhoneMasked: MaskPhone(r.Phone),
			}
			byUser[r.UserID] = rec
			order = append(order, r.UserID)
		}
		rec.Participations = append(rec.Participations, CrossIdentityParticipation{
			TenantID:           r.TenantID,
			BusinessName:       r.BusinessName,
			Role:               r.Role,
			SubscriptionStatus: r.SubscriptionStatus,
			IsPremium:          isPremiumStatus(r.SubscriptionStatus),
		})
	}

	out := make([]CrossIdentityRecord, 0, len(order))
	for _, uid := range order {
		rec := byUser[uid]
		rec.WorkspaceCount = len(rec.Participations)
		rec.EvasionAlert, rec.EvasionReason = detectEvasion(rec.Participations)
		out = append(out, *rec)
	}
	return out
}

func isPremiumStatus(status string) bool {
	return status == models.SubscriptionStatusProActive ||
		status == models.SubscriptionStatusTrial
}

// detectEvasion fires the ⚠️ flag when a user is:
//   (a) an Owner in at least one FREE/PRO_PAST_DUE tenant, AND
//   (b) a non-Owner employee in at least one PRO_ACTIVE tenant.
//
// That combo is suspicious because the real business could be the
// non-paying tenant hiding behind someone else's PRO subscription, or
// vice versa — the owner of a real PRO business works a side gig under
// their own FREE tenant to avoid paying twice. Either way it warrants
// a manual look from the ops team.
func detectEvasion(parts []CrossIdentityParticipation) (bool, string) {
	var ownerInFree bool
	var staffInPro bool
	for _, p := range parts {
		isOwner := p.Role == string(models.RoleOwner)
		nonPremium := p.SubscriptionStatus == models.SubscriptionStatusFree ||
			p.SubscriptionStatus == models.SubscriptionStatusProPastDue
		if isOwner && nonPremium {
			ownerInFree = true
		}
		if !isOwner && p.SubscriptionStatus == models.SubscriptionStatusProActive {
			staffInPro = true
		}
	}
	if ownerInFree && staffInPro {
		return true, "Owner en tenant gratis + empleado en tenant PRO — posible evasión"
	}
	return false, ""
}

// AdminCrossIdentities lists every user (by phone uniqueness) that
// participates in more than one tenant. Response envelope follows
// the rest of the admin API: `{ "data": [...] }`.
func AdminCrossIdentities(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Single query: join users → user_workspaces → tenants
		// → tenant_subscriptions, filtered to users with >1 workspace.
		// Ordering stabilises the dashboard rows across polls.
		sub := db.Model(&models.UserWorkspace{}).
			Select("user_id").
			Group("user_id").
			Having("COUNT(DISTINCT tenant_id) > 1")

		var rows []CrossIdentityRow
		err := db.Table("user_workspaces AS uw").
			Select(`uw.user_id              AS user_id,
			        u.phone                 AS phone,
			        uw.tenant_id            AS tenant_id,
			        t.business_name         AS business_name,
			        uw.role                 AS role,
			        COALESCE(ts.status, '') AS subscription_status`).
			Joins("JOIN users u ON u.id = uw.user_id").
			Joins("JOIN tenants t ON t.id = uw.tenant_id").
			Joins("LEFT JOIN tenant_subscriptions ts ON ts.tenant_id = uw.tenant_id").
			Where("uw.user_id IN (?)", sub).
			Where("u.deleted_at IS NULL AND t.deleted_at IS NULL").
			Order("uw.user_id, t.business_name").
			Scan(&rows).Error
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "error al obtener identidades cruzadas",
			})
			return
		}

		records := BuildCrossIdentityRecords(rows)
		c.JSON(http.StatusOK, gin.H{"data": records})
	}
}

// AdminEcosystemMetrics returns the headline KPIs for the ecosystem
// tab: fiado open/closed counts, online-order volume, outstanding
// debt aggregated across every tenant.
//
// All queries run in parallel-friendly sequential order; at 10k
// tenants each aggregate returns in a single index scan so no
// pagination or caching is needed yet.
func AdminEcosystemMetrics(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var open, closed, onlineCount, outstanding int64

		if err := db.Model(&models.CreditAccount{}).
			Where("status IN ('open', 'partial')").
			Count(&open).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error métricas fiado"})
			return
		}
		if err := db.Model(&models.CreditAccount{}).
			Where("status = 'closed' OR status = 'paid'").
			Count(&closed).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error métricas fiado"})
			return
		}
		if err := db.Model(&models.OnlineOrder{}).
			Count(&onlineCount).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error métricas pedidos"})
			return
		}
		// Outstanding debt = SUM(total - paid) for open/partial accounts.
		// CreditAccount stores both fields as BIGINT (COP cents since
		// migration 016); a negative result would indicate a data
		// integrity issue — clamp at zero so the dashboard can't render
		// a misleading negative.
		row := struct {
			Total int64
		}{}
		db.Model(&models.CreditAccount{}).
			Select("COALESCE(SUM(total_amount - paid_amount), 0) AS total").
			Where("status IN ('open', 'partial')").
			Scan(&row)
		outstanding = row.Total
		if outstanding < 0 {
			outstanding = 0
		}

		c.JSON(http.StatusOK, EcosystemMetrics{
			FiadoOpenCount:     open,
			FiadoClosedCount:   closed,
			OnlineOrdersCount:  onlineCount,
			OutstandingDebtCOP: outstanding,
		})
	}
}
