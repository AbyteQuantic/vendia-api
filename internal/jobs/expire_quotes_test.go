// Spec: specs/031-cotizaciones/spec.md
package jobs_test

import (
	"testing"
	"time"

	"vendia-backend/internal/jobs"
	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupJobsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Quote{}, &models.QuoteItem{}))
	return db
}

// seedQuote inserts a quote with an explicit status + valid_until.
func seedQuote(t *testing.T, db *gorm.DB, tenantID, status string, validUntil time.Time) string {
	t.Helper()
	q := models.Quote{
		TenantID:    tenantID,
		CustomerID:  "00000000-0000-0000-0000-0000000000c1",
		Folio:       "COT-2026-0001",
		Status:      status,
		ValidUntil:  validUntil,
		PublicToken: "11111111-1111-1111-1111-" + randSuffix(),
	}
	require.NoError(t, db.Create(&q).Error)
	return q.ID
}

var suffixCounter int

func randSuffix() string {
	suffixCounter++
	s := []byte("000000000000")
	n := suffixCounter
	i := len(s) - 1
	for n > 0 && i >= 0 {
		s[i] = byte('0' + n%10)
		n /= 10
		i--
	}
	return string(s)
}

// TestExpireQuotes_OnlySentPastValidity verifies the job flips exactly
// the `enviada` quotes whose valid_until is in the past (Spec F031 T-20).
func TestExpireQuotes_OnlySentPastValidity(t *testing.T) {
	db := setupJobsDB(t)
	const tenantID = "22222222-2222-2222-2222-222222222222"

	past := time.Now().UTC().Add(-24 * time.Hour)
	future := time.Now().UTC().Add(24 * time.Hour)

	expiredID := seedQuote(t, db, tenantID, models.QuoteStatusSent, past)
	stillValidID := seedQuote(t, db, tenantID, models.QuoteStatusSent, future)
	draftPastID := seedQuote(t, db, tenantID, models.QuoteStatusDraft, past)
	approvedPastID := seedQuote(t, db, tenantID, models.QuoteStatusApproved, past)

	n, err := jobs.ExpireQuotes(db)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n, "solo 1 cotización debe vencerse")

	status := func(id string) string {
		var q models.Quote
		require.NoError(t, db.Where("id = ?", id).First(&q).Error)
		return q.Status
	}
	assert.Equal(t, models.QuoteStatusExpired, status(expiredID),
		"enviada + vencida → vencida")
	assert.Equal(t, models.QuoteStatusSent, status(stillValidID),
		"enviada con vigencia futura no se toca")
	assert.Equal(t, models.QuoteStatusDraft, status(draftPastID),
		"un borrador vencido NO se marca vencida")
	assert.Equal(t, models.QuoteStatusApproved, status(approvedPastID),
		"una aprobada vencida NO se marca vencida")
}

// TestExpireQuotes_NoMatches verifies the job is a clean no-op when
// nothing qualifies.
func TestExpireQuotes_NoMatches(t *testing.T) {
	db := setupJobsDB(t)
	seedQuote(t, db, "33333333-3333-3333-3333-333333333333",
		models.QuoteStatusSent, time.Now().UTC().Add(48*time.Hour))

	n, err := jobs.ExpireQuotes(db)
	require.NoError(t, err)
	assert.EqualValues(t, 0, n)
}
