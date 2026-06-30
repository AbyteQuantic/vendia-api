package services

import (
	"net"
	"sync"
	"testing"
	"time"

	"vendia-backend/internal/config"
	"vendia-backend/internal/database"
	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupCreditPostgres connects to the Docker Postgres used by the rest of
// the integration suite, or skips when it is not running. The lost-update
// race this test proves can only be exercised against real row locks —
// SQLite's :memory: driver serializes every writer globally, which would
// hide the bug instead of reproducing it.
func setupCreditPostgres(t *testing.T) *gorm.DB {
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
	require.NoError(t, db.AutoMigrate(&models.CreditAccount{}, &models.CreditPayment{}))
	return db
}

// TestRegisterPaymentWithActor_Concurrent_NeverOverpays fires two abonos of
// 6000 at the same instant against a credit with remaining=10000. Only ONE
// can fit once the first commits (remaining drops to 4000), so exactly one
// must succeed and the other must be rejected with ErrPaymentExceeds.
//
// Before the fix, RegisterPaymentWithActor reads the account with a plain
// (unlocked) First() outside any transaction, so both goroutines can read
// the same stale paid_amount=0, both pass the "amount <= remaining" check,
// and both commit a CreditPayment row — a lost update: the sum of
// credit_payments.amount can exceed credit_accounts.paid_amount, and a
// fiado account can be overpaid without ever surfacing ErrPaymentExceeds.
func TestRegisterPaymentWithActor_Concurrent_NeverOverpays(t *testing.T) {
	db := setupCreditPostgres(t)
	svc := NewCreditService(db)

	tenantID := "55555555-5555-5555-5555-555555555555"
	creditID := "66666666-6666-6666-6666-666666666666"
	customerID := "77777777-7777-7777-7777-777777777777"

	require.NoError(t, db.Where("tenant_id = ?", tenantID).Delete(&models.CreditAccount{}).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO credit_accounts
			(id, created_at, updated_at, tenant_id, customer_id, total_amount, paid_amount, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		creditID, time.Now(), time.Now(), tenantID, customerID, int64(10000), int64(0), "open").Error)

	const goroutines = 2
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	successes := 0

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.RegisterPaymentWithActor(tenantID, creditID, "", "", 6000, "cash", "abono concurrente", "")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			} else {
				successes++
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, successes, "exactamente un abono de 6000 debe caber en un saldo de 10000")
	require.Len(t, errs, 1, "el segundo abono debe rechazarse")
	assert.ErrorIs(t, errs[0], ErrPaymentExceeds)

	var account models.CreditAccount
	require.NoError(t, db.Where("id = ?", creditID).First(&account).Error)
	assert.LessOrEqual(t, account.PaidAmount, account.TotalAmount,
		"paid_amount nunca debe superar total_amount")

	var sumPaid int64
	require.NoError(t, db.Model(&models.CreditPayment{}).
		Where("credit_account_id = ?", creditID).
		Select("COALESCE(SUM(amount), 0)").Scan(&sumPaid).Error)
	assert.Equal(t, account.PaidAmount, sumPaid,
		"la suma de credit_payments debe coincidir siempre con paid_amount (sin lost update)")
}
