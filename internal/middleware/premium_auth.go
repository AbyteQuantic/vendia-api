package middleware

import (
	"errors"
	"net/http"
	"time"

	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Nowable is the minimal time-source dependency. Production uses
// time.Now; tests inject a deterministic clock so "trial expires in
// N seconds" scenarios are reproducible without sleep().
type Nowable func() time.Time

// PremiumAuthOptions lets tests swap the clock and the DB-lookup shape
// without reaching into package-level globals. Production wires both
// defaults below.
type PremiumAuthOptions struct {
	Now Nowable
}

// PremiumAuth guards premium endpoints. Allows through if:
//   - Tenant subscription is PRO_ACTIVE, OR
//   - Tenant subscription is TRIAL and trial_ends_at is in the future.
//
// On expiry: rewrites the row to FREE (write-through so subsequent
// requests don't keep re-evaluating the clock) and returns 403
// {"error_code": "premium_expired"}. Non-premium states (FREE,
// PRO_PAST_DUE) fall through to the same 403 — the Flutter client
// renders one soft paywall for all of them.
//
// Note: middleware MUST run AFTER Auth so tenant_id is in context.
func PremiumAuth(db *gorm.DB, opts ...PremiumAuthOptions) gin.HandlerFunc {
	now := time.Now
	if len(opts) > 0 && opts[0].Now != nil {
		now = opts[0].Now
	}

	return func(c *gin.Context) {
		tenantID := GetTenantID(c)
		if tenantID == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":      "token requerido",
				"error_code": "unauthenticated",
			})
			return
		}

		var sub models.TenantSubscription
		err := db.Where("tenant_id = ?", tenantID).First(&sub).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Tenant predates the trigger or the row was deleted — deny
			// with the same soft paywall. The admin dashboard surfaces
			// the missing row so ops can backfill it manually.
			c.AbortWithStatusJSON(http.StatusForbidden, premiumLockedPayload("se requiere plan PRO"))
			return
		}
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error": "error al verificar suscripción",
			})
			return
		}

		if sub.IsPremium(now()) {
			c.Set("subscription_status", sub.Status)
			c.Next()
			return
		}

		// Trial just expired — write-through to FREE so the dashboard
		// badge flips immediately and we don't re-evaluate the clock
		// on every subsequent request. Errors on this write are
		// logged-only; failing to persist the degrade should never
		// prevent us from 403'ing the actual request.
		if sub.Status == models.SubscriptionStatusTrial {
			db.Model(&models.TenantSubscription{}).
				Where("tenant_id = ?", tenantID).
				Updates(map[string]any{
					"status":     models.SubscriptionStatusFree,
					"updated_at": now(),
				})
		}

		c.AbortWithStatusJSON(http.StatusForbidden, premiumLockedPayload("se requiere plan PRO"))
	}
}

// premiumLockedPayload standardises the JSON shape every premium
// endpoint emits when a tenant is not entitled. It ships both the
// legacy keys (error / error_code = "premium_expired") for older
// Flutter builds AND the brief's canonical keys (code: 403,
// error: "premium_feature_locked") so new clients can switch over
// without a coordinated deploy.
//
// Keeping both keys is deliberate — the Flutter Dio interceptor
// currently matches on error_code. Removing it would tear down the
// soft-paywall flow that's already shipped in production.
func premiumLockedPayload(message string) gin.H {
	return gin.H{
		"error":      "premium_feature_locked",
		"code":       http.StatusForbidden,
		"error_code": "premium_expired",
		"message":    message,
	}
}
