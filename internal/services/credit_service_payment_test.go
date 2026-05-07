package services

import (
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupPaymentDB hand-crafts the SQLite schema for the credit-service
// payment tests. CreditAccount in production carries Postgres-only
// defaults so we DDL the bare minimum the service touches. Same
// pattern as branch_isolation_test.go in handlers.
func setupPaymentDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	stmts := []string{
		`CREATE TABLE credit_accounts (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			created_by TEXT, branch_id TEXT,
			customer_id TEXT NOT NULL,
			sale_id TEXT,
			total_amount INTEGER NOT NULL DEFAULT 0,
			paid_amount INTEGER DEFAULT 0,
			description TEXT DEFAULT '',
			status TEXT DEFAULT 'open',
			due_date DATETIME,
			closed_at DATETIME,
			fiado_token TEXT DEFAULT '',
			fiado_status TEXT DEFAULT 'none',
			accepted_at DATETIME,
			accepted_ip TEXT DEFAULT ''
		)`,
		`CREATE TABLE credit_payments (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, credit_account_id TEXT NOT NULL,
			created_by TEXT, branch_id TEXT,
			amount INTEGER NOT NULL DEFAULT 0,
			payment_method TEXT DEFAULT 'cash',
			note TEXT DEFAULT ''
		)`,
	}
	for _, s := range stmts {
		require.NoError(t, db.Exec(s).Error)
	}
	return db
}

// TestRegisterPayment_StampsClosedAt asserts the ledger stamps
// closed_at when a payment settles the full balance — the timestamp
// drives the "Pagados" tab order on the cuaderno screen and must
// never be left null on a paid account.
func TestRegisterPayment_StampsClosedAt(t *testing.T) {
	db := setupPaymentDB(t)

	tenantID := "tenant-payment"
	creditID := "11111111-1111-1111-1111-111111111111"

	require.NoError(t, db.Exec(`
		INSERT INTO credit_accounts
			(id, created_at, updated_at, tenant_id, customer_id, total_amount, paid_amount, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		creditID, time.Now(), time.Now(), tenantID,
		"22222222-2222-2222-2222-222222222222", 10000, 7000, "partial").Error)

	svc := NewCreditService(db)
	before := time.Now().Add(-1 * time.Second)
	payment, err := svc.RegisterPaymentWithActor(tenantID, creditID,
		"", "", 3000, "cash", "saldo final")
	require.NoError(t, err)
	require.NotNil(t, payment)
	assert.EqualValues(t, 3000, payment.Amount)

	var row struct {
		Status     string
		PaidAmount int64      `gorm:"column:paid_amount"`
		ClosedAt   *time.Time `gorm:"column:closed_at"`
	}
	require.NoError(t, db.Table("credit_accounts").
		Select("status, paid_amount, closed_at").
		Where("id = ?", creditID).
		Scan(&row).Error)

	assert.Equal(t, "paid", row.Status, "balance reached zero ⇒ status flips to paid")
	assert.EqualValues(t, 10000, row.PaidAmount)
	require.NotNil(t, row.ClosedAt, "closed_at must be stamped on full settlement")
	assert.True(t, row.ClosedAt.After(before),
		"closed_at must be after the pre-call timestamp")
}

// TestRegisterPayment_PartialDoesNotStampClosedAt is the negative
// pair: a partial abono updates the balance and status='partial'
// but MUST leave closed_at null — closing happens only at zero.
func TestRegisterPayment_PartialDoesNotStampClosedAt(t *testing.T) {
	db := setupPaymentDB(t)

	tenantID := "tenant-partial"
	creditID := "33333333-3333-3333-3333-333333333333"

	require.NoError(t, db.Exec(`
		INSERT INTO credit_accounts
			(id, created_at, updated_at, tenant_id, customer_id, total_amount, paid_amount, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		creditID, time.Now(), time.Now(), tenantID,
		"44444444-4444-4444-4444-444444444444", 10000, 0, "open").Error)

	svc := NewCreditService(db)
	_, err := svc.RegisterPaymentWithActor(tenantID, creditID,
		"", "", 4000, "cash", "abono parcial")
	require.NoError(t, err)

	var row struct {
		Status   string
		ClosedAt *time.Time `gorm:"column:closed_at"`
	}
	require.NoError(t, db.Table("credit_accounts").
		Select("status, closed_at").
		Where("id = ?", creditID).
		Scan(&row).Error)

	assert.Equal(t, "partial", row.Status)
	assert.Nil(t, row.ClosedAt,
		"closed_at must remain null while there's still a balance")
}

// Compile-time check: ensure the model carries the new column so a
// silent removal of CreditAccount.ClosedAt in a future refactor
// breaks this build instead of the tests in another package.
var _ *time.Time = (&models.CreditAccount{}).ClosedAt
