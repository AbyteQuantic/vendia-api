// Spec: specs/025-captcha-pedidos-publicos/spec.md — F025 rate-limit hardening.
package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
)

func setupAccountWSTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.OrderTicket{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestVerifyAccountPhone_RateLimited reproduces the exact wiring main.go
// uses for POST /api/v1/account/:order_uuid/verify: the dedicated
// loginLimiter sits in front of the handler. Before this fix the route
// was registered directly on the bare gin.Engine with NO middleware at
// all (the v1 group's globalLimiter only wraps routes added through the
// v1 group object, not routes already bound on the bare engine) — an
// attacker could brute-force the customer's phone number on a public
// account/bill link with zero throttle. A burst of requests beyond the
// configured limit must now receive 429, exactly like /login.
func TestVerifyAccountPhone_RateLimited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupAccountWSTestDB(t)

	const limit = 5
	loginLimiter := middleware.NewRateLimiter(limit, 1*time.Minute)

	r := gin.New()
	r.POST("/api/v1/account/:order_uuid/verify",
		loginLimiter, VerifyAccountPhone(db))

	doAttempt := func(phone string) int {
		body, _ := json.Marshal(map[string]string{"phone": phone})
		req, _ := http.NewRequest(http.MethodPost,
			"/api/v1/account/does-not-exist/verify", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// The first `limit` brute-force attempts are let through by the
	// limiter (the handler itself 404s because the order doesn't
	// exist — that's expected and irrelevant to this test, which only
	// cares that the limiter hasn't kicked in yet).
	for i := 1; i <= limit; i++ {
		code := doAttempt("300000000" + string(rune('0'+i)))
		assert.NotEqual(t, http.StatusTooManyRequests, code,
			"attempt %d should not be throttled yet", i)
	}

	// The next attempt — beyond the limit, same IP — must be blocked.
	code := doAttempt("3009999999")
	assert.Equal(t, http.StatusTooManyRequests, code,
		"brute-force attempt beyond the limit must receive 429")
}
