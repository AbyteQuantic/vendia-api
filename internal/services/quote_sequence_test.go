// Spec: specs/031-cotizaciones/spec.md
package services_test

import (
	"net"
	"sync"
	"testing"
	"time"

	"vendia-backend/internal/config"
	"vendia-backend/internal/database"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupSequenceSQLite gives an in-memory SQLite DB with the
// QuoteSequence table — enough for the single-threaded "first folio"
// assertion. The concurrency test below needs real Postgres row locks.
func setupSequenceSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.QuoteSequence{}))
	return db
}

// setupSequencePostgres connects to the Docker Postgres used by the rest
// of the integration suite, or skips when it is not running. SELECT FOR
// UPDATE is a no-op on SQLite, so the concurrency guarantee can only be
// proven against Postgres.
func setupSequencePostgres(t *testing.T) *gorm.DB {
	t.Helper()
	conn, err := net.DialTimeout("tcp", "localhost:5499", 1*time.Second)
	if err != nil {
		t.Skip("Skipping: Docker PostgreSQL not available (run 'make local')")
	}
	conn.Close()

	cfg := &config.Config{
		DatabaseURL: "postgres://vendia:vendia_secret@localhost:5499/vendia?sslmode=disable",
		JWTSecret:   "test-jwt-secret-vendia-2024-long-enough-32",
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Skip("Skipping: Docker PostgreSQL not available")
	}
	require.NoError(t, db.AutoMigrate(&models.QuoteSequence{}))
	return db
}

// TestNextQuoteFolio_FirstAndConsecutive verifies a brand-new tenant
// gets COT-YYYY-0001 and subsequent calls increment (Spec F031 T-05).
func TestNextQuoteFolio_FirstAndConsecutive(t *testing.T) {
	db := setupSequenceSQLite(t)
	const tenantID = "11111111-1111-1111-1111-111111111111"

	var got []string
	for i := 0; i < 3; i++ {
		err := db.Transaction(func(tx *gorm.DB) error {
			folio, err := services.NextQuoteFolio(tx, tenantID, 2026)
			if err != nil {
				return err
			}
			got = append(got, folio)
			return nil
		})
		require.NoError(t, err)
	}

	assert.Equal(t, []string{"COT-2026-0001", "COT-2026-0002", "COT-2026-0003"}, got)
}

// TestNextQuoteFolio_PerTenantPerYear verifies counters are independent
// across tenants and years (Spec plan D2).
func TestNextQuoteFolio_PerTenantPerYear(t *testing.T) {
	db := setupSequenceSQLite(t)
	const tenantA = "22222222-2222-2222-2222-222222222222"
	const tenantB = "33333333-3333-3333-3333-333333333333"

	folio := func(tenant string, year int) string {
		var out string
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			f, err := services.NextQuoteFolio(tx, tenant, year)
			out = f
			return err
		}))
		return out
	}

	assert.Equal(t, "COT-2026-0001", folio(tenantA, 2026))
	assert.Equal(t, "COT-2026-0001", folio(tenantB, 2026), "tenant B has its own counter")
	assert.Equal(t, "COT-2026-0002", folio(tenantA, 2026))
	assert.Equal(t, "COT-2027-0001", folio(tenantA, 2027), "new year resets the counter")
}

// TestNextQuoteFolio_Concurrency fires 10 goroutines at the same
// (tenant, year) and asserts 10 distinct, consecutive folios — proving
// the SELECT FOR UPDATE serialisation has no collision (Spec F031 T-05,
// plan R4). Requires Postgres; skips without it.
func TestNextQuoteFolio_Concurrency(t *testing.T) {
	db := setupSequencePostgres(t)
	const tenantID = "44444444-4444-4444-4444-444444444444"
	const year = 2030

	// Clean slate for this tenant-year so the run is deterministic.
	require.NoError(t,
		db.Where("tenant_id = ? AND year = ?", tenantID, year).
			Delete(&models.QuoteSequence{}).Error)

	const goroutines = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	folios := make([]string, 0, goroutines)
	errs := make([]error, 0)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Retry on the rare first-row INSERT race (composite PK
			// collision) — see NextQuoteFolio's comment.
			var folio string
			var err error
			for attempt := 0; attempt < 5; attempt++ {
				err = db.Transaction(func(tx *gorm.DB) error {
					f, e := services.NextQuoteFolio(tx, tenantID, year)
					folio = f
					return e
				})
				if err == nil {
					break
				}
			}
			mu.Lock()
			if err != nil {
				errs = append(errs, err)
			} else {
				folios = append(folios, folio)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	require.Empty(t, errs, "no goroutine should error")
	require.Len(t, folios, goroutines)

	seen := map[string]bool{}
	for _, f := range folios {
		assert.False(t, seen[f], "folio %s handed out twice — collision!", f)
		seen[f] = true
	}
	// 10 unique folios COT-2030-0001 .. COT-2030-0010.
	for n := 1; n <= goroutines; n++ {
		f := "COT-2030-" + pad4(n)
		assert.True(t, seen[f], "expected folio %s in the set", f)
	}
}

// pad4 zero-pads to 4 digits — local helper so the test does not import
// the unexported formatFolio.
func pad4(n int) string {
	s := []byte("0000")
	i := len(s) - 1
	for n > 0 && i >= 0 {
		s[i] = byte('0' + n%10)
		n /= 10
		i--
	}
	return string(s)
}
