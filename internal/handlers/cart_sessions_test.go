package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/auth"
	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Cart-session lock contract:
//
//   1. Claim by user A returns 201 + the row.
//   2. Re-claim by user A refreshes heartbeat, NOT a new row.
//   3. Claim of A's slot by user B returns 409 with the holder's data.
//   4. Release by A clears the slot; B can then claim.
//   5. A heartbeat older than 5 min is pruned before any conflict
//      check, so a crashed phone doesn't lock a cart forever.
//   6. List returns only the caller's tenant rows, branch-aware.

const cartTestSecret = "cart-test-secret-at-least-32-chars-x"

func setupCartDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.CartSession{}))
	return db
}

func mountCartRoutes(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(middleware.Auth(cartTestSecret))
	g.GET("/carts/sessions", handlers.ListCartSessions(db))
	g.POST("/carts/sessions/claim", handlers.ClaimCartSession(db))
	g.POST("/carts/sessions/heartbeat", handlers.HeartbeatCartSession(db))
	g.POST("/carts/sessions/release", handlers.ReleaseCartSession(db))
	return r
}

func tokenFor(t *testing.T, userID, tenantID string) string {
	t.Helper()
	tok, err := auth.GenerateWorkspaceToken(
		userID, tenantID, "", "+57"+userID[:6], "Test Biz", "cashier", cartTestSecret)
	require.NoError(t, err)
	return tok
}

func doCartJSON(t *testing.T, r *gin.Engine, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestClaim_FreshSlot_Returns201(t *testing.T) {
	db := setupCartDB(t)
	r := mountCartRoutes(db)
	tok := tokenFor(t, uuid.NewString(), uuid.NewString())

	w := doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/claim", tok,
		map[string]int{"cart_index": 0})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp struct {
		Data handlers.CartSessionView `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Data.CartIndex)
}

func TestClaim_ZeroIndex_Accepted(t *testing.T) {
	// Cart 0 is the default tab — must be claimable. Regression test
	// for a `binding:"required"` typo that was rejecting zero values.
	db := setupCartDB(t)
	r := mountCartRoutes(db)
	tok := tokenFor(t, uuid.NewString(), uuid.NewString())

	for _, idx := range []int{0, 5, 9} {
		w := doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/claim", tok,
			map[string]int{"cart_index": idx})
		assert.Equal(t, http.StatusCreated, w.Code,
			fmt.Sprintf("idx=%d should be accepted, body=%s", idx, w.Body.String()))
	}
}

func TestClaim_OutOfRange_Returns400(t *testing.T) {
	db := setupCartDB(t)
	r := mountCartRoutes(db)
	tok := tokenFor(t, uuid.NewString(), uuid.NewString())
	for _, idx := range []int{-1, 10, 99} {
		w := doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/claim", tok,
			map[string]int{"cart_index": idx})
		assert.Equal(t, http.StatusBadRequest, w.Code, "idx=%d", idx)
	}
}

func TestClaim_Refresh_SameUserKeepsRow(t *testing.T) {
	db := setupCartDB(t)
	r := mountCartRoutes(db)
	userA := uuid.NewString()
	tenantA := uuid.NewString()
	tok := tokenFor(t, userA, tenantA)

	w1 := doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/claim", tok,
		map[string]int{"cart_index": 2})
	require.Equal(t, http.StatusCreated, w1.Code)

	// Same user re-claims → 200, NOT 409, NOT a duplicate row.
	w2 := doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/claim", tok,
		map[string]int{"cart_index": 2})
	assert.Equal(t, http.StatusOK, w2.Code)

	var count int64
	db.Model(&models.CartSession{}).Where("tenant_id = ?", tenantA).Count(&count)
	assert.Equal(t, int64(1), count, "no duplicate rows on refresh")
}

func TestClaim_DifferentUser_Returns409WithHolder(t *testing.T) {
	db := setupCartDB(t)
	r := mountCartRoutes(db)
	tenantID := uuid.NewString()
	tokA := tokenFor(t, uuid.NewString(), tenantID)
	tokB := tokenFor(t, uuid.NewString(), tenantID)

	w1 := doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/claim", tokA,
		map[string]int{"cart_index": 3})
	require.Equal(t, http.StatusCreated, w1.Code)

	// Different user same tenant → 409 with holder info.
	w2 := doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/claim", tokB,
		map[string]int{"cart_index": 3})
	require.Equal(t, http.StatusConflict, w2.Code, w2.Body.String())

	var resp struct {
		ErrorCode string                   `json:"error_code"`
		Holder    handlers.CartSessionView `json:"holder"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp))
	assert.Equal(t, "cart_locked", resp.ErrorCode)
	assert.Equal(t, 3, resp.Holder.CartIndex,
		"client must know which slot is locked + by whom")
}

func TestRelease_FreesSlotForOtherUser(t *testing.T) {
	db := setupCartDB(t)
	r := mountCartRoutes(db)
	tenantID := uuid.NewString()
	tokA := tokenFor(t, uuid.NewString(), tenantID)
	tokB := tokenFor(t, uuid.NewString(), tenantID)

	doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/claim", tokA,
		map[string]int{"cart_index": 4})

	// B blocked.
	w := doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/claim", tokB,
		map[string]int{"cart_index": 4})
	require.Equal(t, http.StatusConflict, w.Code)

	// A releases.
	w = doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/release", tokA,
		map[string]int{"cart_index": 4})
	require.Equal(t, http.StatusOK, w.Code)

	// Now B claims fresh.
	w = doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/claim", tokB,
		map[string]int{"cart_index": 4})
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}

func TestRelease_CannotFreeOtherUsersSlot(t *testing.T) {
	db := setupCartDB(t)
	r := mountCartRoutes(db)
	tenantID := uuid.NewString()
	userA := uuid.NewString()
	tokA := tokenFor(t, userA, tenantID)
	tokB := tokenFor(t, uuid.NewString(), tenantID)

	doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/claim", tokA,
		map[string]int{"cart_index": 5})

	// B sends a release for slot 5 (A's). The endpoint must NOT delete A's row.
	doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/release", tokB,
		map[string]int{"cart_index": 5})

	// A's row still there.
	var count int64
	db.Model(&models.CartSession{}).
		Where("tenant_id = ? AND cart_index = ? AND user_id = ?", tenantID, 5, userA).
		Count(&count)
	assert.Equal(t, int64(1), count, "release must not let user B free user A's slot")
}

func TestList_PrunesStaleHeartbeats(t *testing.T) {
	db := setupCartDB(t)
	r := mountCartRoutes(db)
	tenantID := uuid.NewString()
	tok := tokenFor(t, uuid.NewString(), tenantID)

	// Insert a stale row (heartbeat 10 minutes ago).
	stale := models.CartSession{
		TenantID:      tenantID,
		CartIndex:     7,
		UserID:        uuid.NewString(),
		EmployeeName:  "ghost",
		Role:          "cashier",
		StartedAt:     time.Now().Add(-15 * time.Minute),
		LastHeartbeat: time.Now().Add(-10 * time.Minute),
	}
	require.NoError(t, db.Create(&stale).Error)

	w := doCartJSON(t, r, http.MethodGet, "/api/v1/carts/sessions", tok, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data []handlers.CartSessionView `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Data, "stale row must be pruned before list returns")

	// And the row is gone from the DB, not just hidden.
	var count int64
	db.Model(&models.CartSession{}).Where("tenant_id = ?", tenantID).Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestList_ScopedToTenant(t *testing.T) {
	db := setupCartDB(t)
	r := mountCartRoutes(db)
	tenantA := uuid.NewString()
	tenantB := uuid.NewString()
	tokA := tokenFor(t, uuid.NewString(), tenantA)

	// Pre-seed both tenants.
	doCartJSON(t, r, http.MethodPost, "/api/v1/carts/sessions/claim", tokA,
		map[string]int{"cart_index": 1})
	require.NoError(t, db.Create(&models.CartSession{
		TenantID: tenantB, CartIndex: 1, UserID: uuid.NewString(),
		LastHeartbeat: time.Now(), StartedAt: time.Now(),
	}).Error)

	w := doCartJSON(t, r, http.MethodGet, "/api/v1/carts/sessions", tokA, nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []handlers.CartSessionView `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp.Data, 1, "list must not leak the other tenant's row")
}
