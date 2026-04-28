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

// staleAfter is the heartbeat window. A session whose last_heartbeat
// is older than this is considered abandoned (crashed phone, screen
// off, app killed) and freed automatically before any conflict check.
//
// 5 minutes balances "you can't lock a phone with the screen off and
// own a cart for hours" against "a fast cashier closing the screen
// to scan a barcode in another app shouldn't lose their cart".
const staleAfter = 5 * time.Minute

// CartSessionView is the public payload (no internal DB metadata).
type CartSessionView struct {
	CartIndex     int       `json:"cart_index"`
	UserID        string    `json:"user_id"`
	EmployeeName  string    `json:"employee_name"`
	Role          string    `json:"role"`
	StartedAt     time.Time `json:"started_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

func toView(s models.CartSession) CartSessionView {
	return CartSessionView{
		CartIndex:     s.CartIndex,
		UserID:        s.UserID,
		EmployeeName:  s.EmployeeName,
		Role:          s.Role,
		StartedAt:     s.StartedAt,
		LastHeartbeat: s.LastHeartbeat,
	}
}

// pruneStale removes sessions whose heartbeat hasn't refreshed in
// staleAfter. Idempotent — running it on every request keeps the
// snapshot honest without a background job.
func pruneStale(db *gorm.DB, tenantID string) {
	cutoff := time.Now().Add(-staleAfter)
	db.Where("tenant_id = ? AND last_heartbeat < ?", tenantID, cutoff).
		Delete(&models.CartSession{})
}

// findSlot loads the active session (if any) for a (tenant, branch,
// cart_index) tuple. Branch is matched both as NULL and as a value
// because some workspaces still ship without a branch_id.
func findSlot(
	db *gorm.DB,
	tenantID string,
	branchID *string,
	cartIndex int,
) (*models.CartSession, error) {
	q := db.Where("tenant_id = ? AND cart_index = ?", tenantID, cartIndex)
	if branchID == nil || *branchID == "" {
		q = q.Where("branch_id IS NULL")
	} else {
		q = q.Where("branch_id = ?", *branchID)
	}
	var s models.CartSession
	err := q.First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &s, err
}

// ListCartSessions returns the live snapshot for every cart slot the
// caller's tenant currently has held. UI uses this to paint locks
// next to each cart tab.
//
// GET /api/v1/carts/sessions
func ListCartSessions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		pruneStale(db, tenantID)

		branchID := middleware.GetBranchIDPtr(c)
		q := db.Where("tenant_id = ?", tenantID)
		if branchID == nil {
			q = q.Where("branch_id IS NULL")
		} else {
			q = q.Where("branch_id = ?", *branchID)
		}

		var rows []models.CartSession
		if err := q.Order("cart_index asc").Find(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al cargar sesiones"})
			return
		}

		out := make([]CartSessionView, 0, len(rows))
		for _, r := range rows {
			out = append(out, toView(r))
		}
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}

// ClaimCartSession claims (or refreshes) a slot for the caller. If
// the slot is already held by someone else we return 409 with the
// holder's info so the UI can paint the lock badge.
//
// POST /api/v1/carts/sessions/claim
// body: {"cart_index": 0}
func ClaimCartSession(db *gorm.DB) gin.HandlerFunc {
	// Pointer so cart_index=0 (the default tab) decodes as a real
	// zero rather than a missing field — `binding:"required"` would
	// reject the legitimate first cart otherwise.
	type Request struct {
		CartIndex *int `json:"cart_index"`
	}
	return func(c *gin.Context) {
		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.CartIndex == nil || *req.CartIndex < 0 || *req.CartIndex > 9 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cart_index fuera de rango"})
			return
		}
		cartIndex := *req.CartIndex

		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		if userID == "" {
			// Legacy single-tenant tokens use tenant_id as the principal
			// id. Without a userID we can't attribute the lock; fall back
			// to the tenant id so legacy owners still work.
			userID = tenantID
		}
		branchID := middleware.GetBranchIDPtr(c)
		role := middleware.GetRole(c)
		empName := c.GetString("phone") // best-effort; UI shows fallback

		pruneStale(db, tenantID)

		// Conflict check first — we return 409 even for the same user
		// when the existing row has a different user_id, so the client
		// can render "ocupado por X".
		existing, err := findSlot(db, tenantID, branchID, cartIndex)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al consultar sesión"})
			return
		}

		now := time.Now()
		if existing != nil {
			if existing.UserID != userID {
				c.JSON(http.StatusConflict, gin.H{
					"error":      "cuenta en uso",
					"error_code": "cart_locked",
					"holder":     toView(*existing),
				})
				return
			}
			// Same user — refresh heartbeat. Keeps StartedAt for analytics.
			existing.LastHeartbeat = now
			existing.EmployeeName = empName
			existing.Role = role
			if err := db.Save(existing).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al refrescar sesión"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"data": toView(*existing)})
			return
		}

		// Fresh claim.
		row := models.CartSession{
			TenantID:      tenantID,
			BranchID:      branchID,
			CartIndex:     cartIndex,
			UserID:        userID,
			EmployeeName:  empName,
			Role:          role,
			StartedAt:     now,
			LastHeartbeat: now,
		}
		if err := db.Create(&row).Error; err != nil {
			// Race: another device beat us to it. Re-read and 409.
			if again, _ := findSlot(db, tenantID, branchID, cartIndex); again != nil {
				c.JSON(http.StatusConflict, gin.H{
					"error":      "cuenta en uso",
					"error_code": "cart_locked",
					"holder":     toView(*again),
				})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al reservar sesión"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": toView(row)})
	}
}

// HeartbeatCartSession refreshes the held slot's heartbeat. Acts as
// claim if the slot is empty (e.g. it was pruned while the cashier
// was idle). Returns 409 if another user owns it now.
//
// POST /api/v1/carts/sessions/heartbeat
// body: {"cart_index": 0}
func HeartbeatCartSession(db *gorm.DB) gin.HandlerFunc {
	// Same payload as Claim — re-uses the conflict semantics.
	return ClaimCartSession(db)
}

// ReleaseCartSession deletes the caller's hold on a slot. Other
// users' rows are untouched (a stray request can't free someone
// else's cart).
//
// POST /api/v1/carts/sessions/release
// body: {"cart_index": 0}
func ReleaseCartSession(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		CartIndex int `json:"cart_index"`
	}
	return func(c *gin.Context) {
		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.CartIndex < 0 || req.CartIndex > 9 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cart_index fuera de rango"})
			return
		}

		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		if userID == "" {
			userID = tenantID
		}
		branchID := middleware.GetBranchIDPtr(c)

		q := db.Where("tenant_id = ? AND cart_index = ? AND user_id = ?",
			tenantID, req.CartIndex, userID)
		if branchID == nil {
			q = q.Where("branch_id IS NULL")
		} else {
			q = q.Where("branch_id = ?", *branchID)
		}
		q.Delete(&models.CartSession{})
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

