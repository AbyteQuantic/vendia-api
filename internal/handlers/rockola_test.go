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

func setupRockolaTestDB(t *testing.T) (*gorm.DB, models.Tenant) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Tenant{}, &models.RockolaSuggestion{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := "tienda"
	tenant := models.Tenant{
		BaseModel:    models.BaseModel{ID: "tenant-1"},
		BusinessName: "Tienda Test",
		Phone:        "3000000000",
		StoreSlug:    &s,
	}
	if err := db.Create(&tenant).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return db, tenant
}

// TestSuggestSong_RateLimited reproduces main.go's wiring for
// POST /api/v1/rockola/:slug/suggest: the dedicated orderRateLimiter
// (5 req / 15 min / IP, F025) sits in front of the handler. Before this
// fix the route was bound directly on the bare gin.Engine with NO
// middleware at all (the v1 group's globalLimiter never wraps routes
// already registered on the bare engine) — a customer device could spam
// song suggestions with zero throttle. A burst beyond the limit must now
// receive 429.
func TestSuggestSong_RateLimited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, _ := setupRockolaTestDB(t)

	const limit = 5
	orderRateLimiter := middleware.NewRateLimiter(limit, 15*time.Minute)

	r := gin.New()
	r.POST("/api/v1/rockola/:slug/suggest", orderRateLimiter, SuggestSong(db))

	suggest := func(track string) int {
		body, _ := json.Marshal(map[string]string{
			"track_name":  track,
			"artist_name": "Artista",
		})
		req, _ := http.NewRequest(http.MethodPost,
			"/api/v1/rockola/tienda/suggest", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	for i := 1; i <= limit; i++ {
		code := suggest("Canción " + string(rune('0'+i)))
		assert.Equal(t, http.StatusCreated, code, "suggestion %d should succeed", i)
	}

	code := suggest("Canción de más")
	assert.Equal(t, http.StatusTooManyRequests, code,
		"a 6th suggestion from the same IP within the window must receive 429")
}
