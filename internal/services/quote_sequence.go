// Spec: specs/031-cotizaciones/spec.md
package services

import (
	"fmt"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NextQuoteFolio atomically reserves and returns the next folio for a
// tenant + year, formatted COT-YYYY-NNNN (Spec F031 AC-04).
//
// Concurrency contract (Spec plan D2 + R4): the read of the counter and
// its increment happen inside one transaction with a row-level write
// lock (SELECT ... FOR UPDATE). Two requests racing for the same
// (tenant, year) are serialised by Postgres — each gets a distinct,
// consecutive number, never a collision.
//
// The first quote of a (tenant, year) has no QuoteSequence row yet, so
// we upsert one with NextValue starting at 1. clause.Locking is a no-op
// on SQLite (unit tests) — SQLite serialises writers anyway, so the test
// suite still observes unique folios without Postgres.
//
// tx MUST be an open transaction handle. The caller (CreateQuote) wraps
// folio assignment + quote insert in the same transaction so a failure
// rolls the counter back.
func NextQuoteFolio(tx *gorm.DB, tenantID string, year int) (string, error) {
	var seq models.QuoteSequence

	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND year = ?", tenantID, year).
		First(&seq).Error

	switch {
	case err == nil:
		// Row exists and is now locked — hand out NextValue, then bump.
		current := seq.NextValue
		if err := tx.Model(&models.QuoteSequence{}).
			Where("tenant_id = ? AND year = ?", tenantID, year).
			UpdateColumn("next_value", gorm.Expr("next_value + 1")).Error; err != nil {
			return "", fmt.Errorf("could not bump quote sequence: %w", err)
		}
		return formatFolio(year, current), nil

	case err == gorm.ErrRecordNotFound:
		// First quote for this (tenant, year). Insert the counter row
		// already advanced to 2 so this caller takes number 1. A
		// concurrent racer that also missed the row will collide on the
		// composite primary key and its INSERT fails — the caller
		// retries the whole transaction. In practice the very first
		// quote of a tenant-year is not a hot path.
		seq = models.QuoteSequence{TenantID: tenantID, Year: year, NextValue: 2}
		if err := tx.Create(&seq).Error; err != nil {
			return "", fmt.Errorf("could not create quote sequence: %w", err)
		}
		return formatFolio(year, 1), nil

	default:
		return "", fmt.Errorf("could not read quote sequence: %w", err)
	}
}

// formatFolio renders a folio as COT-YYYY-NNNN (4-digit zero-padded).
func formatFolio(year, n int) string {
	return fmt.Sprintf("COT-%d-%04d", year, n)
}
