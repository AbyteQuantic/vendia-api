package handlers

import (
	"strings"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// BranchScopeResolution captures the result of deciding which sede
// a request is scoped to. The three-state return is deliberate:
//
//   - BranchID != "" → handler must add `WHERE branch_id = ?` to
//     every query. Either the client explicitly passed ?branch_id=
//     or the caller's JWT has a workspace-scoped claim.
//   - BranchID == "" and NotOwned is false → "no scope" — the
//     caller doesn't know or doesn't care, so we stay backward-compat
//     and return all rows for the tenant. Matches the pre-Phase-5
//     behaviour so mono-sede tenants keep working.
//   - NotOwned == true → the client asked for a branch that doesn't
//     belong to their tenant. Handler should 403 before issuing
//     the query — otherwise a crafted URL could exfiltrate row
//     counts even when the main SELECT returns zero.
type BranchScopeResolution struct {
	BranchID string
	NotOwned bool
}

// ResolveBranchScope picks the branch to filter by. Priority:
//
//  1. ?branch_id= query param (explicit client override — the sede
//     selector in Flutter uses this)
//  2. JWT's branch claim (workspace-scoped employee tokens)
//
// The "none" case is valid and intentional: the first version of
// the POS shipped without workspace tokens, so a global-scope
// response is the backwards-compatible default. Callers that
// require a branch (e.g. CreateSale) must enforce it explicitly.
func ResolveBranchScope(c *gin.Context, db *gorm.DB) BranchScopeResolution {
	tenantID := middleware.GetTenantID(c)

	candidate := strings.TrimSpace(c.Query("branch_id"))
	if candidate == "" {
		candidate = middleware.GetBranchID(c)
	}
	if candidate == "" {
		return BranchScopeResolution{}
	}
	if !models.IsValidUUID(candidate) {
		// Silently ignore garbage — older JWTs occasionally had a
		// blank-string claim that slipped through. Treating it as
		// "no scope" preserves the mono-sede backward-compat path.
		return BranchScopeResolution{}
	}

	// Ownership check — don't let a crafted request scope into a
	// sede that belongs to a different tenant. The check lives here
	// (not per-handler) so every branch-scoped endpoint gets the
	// protection by construction.
	var count int64
	db.Model(&models.Branch{}).
		Where("id = ? AND tenant_id = ?", candidate, tenantID).
		Count(&count)
	if count == 0 {
		return BranchScopeResolution{NotOwned: true}
	}
	return BranchScopeResolution{BranchID: candidate}
}

// ApplyBranchScope tightens a query when the resolution points at
// a specific sede. "No scope" leaves the query untouched — caller
// already added the tenant filter, so the fall-through matches
// the pre-isolation behaviour.
func ApplyBranchScope(q *gorm.DB, scope BranchScopeResolution) *gorm.DB {
	if scope.BranchID == "" {
		return q
	}
	return q.Where("branch_id = ?", scope.BranchID)
}
