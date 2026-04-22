package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupPremiumAuthTest spins up an in-memory SQLite DB with the
// TenantSubscription schema and a Gin router that installs the
// middleware behind a fake Auth middleware that stuffs the provided
// tenant_id into the context. Returns the router, DB, and the clock
// the middleware is bound to — tests mutate the clock pointer to
// move time forward.
func setupPremiumAuthTest(t *testing.T, now time.Time, tenantID string) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.TenantSubscription{}))

	// Purge any rows from a previous subtest — shared-cache SQLite
	// persists across connections in the same process.
	require.NoError(t, db.Exec("DELETE FROM tenant_subscriptions").Error)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		if tenantID != "" {
			c.Set(TenantIDKey, tenantID)
		}
		c.Next()
	})
	r.Use(PremiumAuth(db, PremiumAuthOptions{Now: func() time.Time { return now }}))
	r.GET("/premium", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r, db
}

func seedSubscription(t *testing.T, db *gorm.DB, sub models.TenantSubscription) {
	t.Helper()
	require.NoError(t, db.Create(&sub).Error)
}

func TestPremiumAuth_AllowsActiveTrial(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	tenantID := "tenant-trial-active"
	r, db := setupPremiumAuthTest(t, now, tenantID)

	seedSubscription(t, db, models.TenantSubscription{
		TenantID:    tenantID,
		Status:      models.SubscriptionStatusTrial,
		TrialEndsAt: ptrTime(now.Add(3 * 24 * time.Hour)),
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/premium", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "\"ok\":true")
}

func TestPremiumAuth_AllowsProActive(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	tenantID := "tenant-pro"
	r, db := setupPremiumAuthTest(t, now, tenantID)

	seedSubscription(t, db, models.TenantSubscription{
		TenantID:    tenantID,
		Status:      models.SubscriptionStatusProActive,
		TrialEndsAt: nil,
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/premium", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestPremiumAuth_RejectsExpiredTrialAndWritesThroughToFree(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	tenantID := "tenant-trial-expired"
	r, db := setupPremiumAuthTest(t, now, tenantID)

	// Trial ended 1 hour ago
	seedSubscription(t, db, models.TenantSubscription{
		TenantID:    tenantID,
		Status:      models.SubscriptionStatusTrial,
		TrialEndsAt: ptrTime(now.Add(-1 * time.Hour)),
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/premium", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "premium_expired", body["error_code"])

	// Write-through degrade: subsequent lookups should see FREE
	var sub models.TenantSubscription
	require.NoError(t, db.Where("tenant_id = ?", tenantID).First(&sub).Error)
	assert.Equal(t, models.SubscriptionStatusFree, sub.Status,
		"expired trial must be persisted as FREE so the dashboard flips immediately")
}

func TestPremiumAuth_RejectsFree(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	tenantID := "tenant-free"
	r, db := setupPremiumAuthTest(t, now, tenantID)

	seedSubscription(t, db, models.TenantSubscription{
		TenantID: tenantID,
		Status:   models.SubscriptionStatusFree,
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/premium", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "premium_expired", body["error_code"])
}

func TestPremiumAuth_RejectsPastDue(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	tenantID := "tenant-past-due"
	r, db := setupPremiumAuthTest(t, now, tenantID)

	seedSubscription(t, db, models.TenantSubscription{
		TenantID: tenantID,
		Status:   models.SubscriptionStatusProPastDue,
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/premium", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestPremiumAuth_RejectsMissingSubscription(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	tenantID := "tenant-no-row"
	r, _ := setupPremiumAuthTest(t, now, tenantID)
	// intentionally do NOT seed a row

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/premium", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "premium_expired", body["error_code"])
}

func TestPremiumAuth_RejectsWhenTenantIDMissing(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	r, _ := setupPremiumAuthTest(t, now, "") // empty tenant_id in context

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/premium", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func ptrTime(t time.Time) *time.Time { return &t }
