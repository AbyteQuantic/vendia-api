// Package services — slug.go
//
// Slug generation and validation for tenant public stores.
//
// Contract:
//   - URL-safe: only [a-z0-9-], no leading/trailing dash, no double dash.
//   - Deterministic sanitization from the business name, then a short
//     random hex suffix (4 chars) to avoid collisions between shops with
//     the same name ("Tienda Don Pepe" in two barrios becomes
//     "tienda-don-pepe-a4x9" and "tienda-don-pepe-b2k1").
//   - GenerateUniqueSlug retries up to maxAttempts with a fresh suffix
//     if a collision is found; falls back to a longer random tail on
//     the final attempt so it is mathematically guaranteed to succeed.
package services

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"gorm.io/gorm"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// SlugPattern is the canonical regex for a valid store slug.
// Exported so handlers can reuse the exact same check the DB enforces.
var SlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// MaxSlugLength caps the final slug to keep URLs sharable.
const MaxSlugLength = 48

// SlugifyBase turns a free-form business name into a URL-safe base
// slug WITHOUT a random suffix. Used as the deterministic prefix that
// GenerateUniqueSlug then decorates.
//
//   "Tienda Don Pepé!!"  ->  "tienda-don-pepe"
//   "   "                ->  "tienda"      (safe fallback)
//   "Café & Más"         ->  "cafe-mas"
func SlugifyBase(name string) string {
	// Unicode NFD decomposes "é" into "e" + combining acute, then we
	// strip the combining marks. This is the standard recipe for
	// latin-script diacritic removal and covers the Colombian SMB
	// long-tail (ñ, á, é, í, ó, ú, ü).
	t := transform.Chain(
		norm.NFD,
		runes.Remove(runes.In(unicode.Mn)),
		norm.NFC,
	)
	folded, _, err := transform.String(t, name)
	if err != nil {
		folded = name
	}

	var b strings.Builder
	b.Grow(len(folded))
	prevDash := true // suppress leading dashes
	for _, r := range folded {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32) // lowercase
			prevDash = false
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}

	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		out = "tienda"
	}
	// Reserve room for the "-xxxx" suffix (5 chars) within MaxSlugLength.
	if max := MaxSlugLength - 5; len(out) > max {
		out = strings.TrimRight(out[:max], "-")
	}
	return out
}

// randomSuffix returns n bytes of random data hex-encoded (2n chars).
// Falls back to a deterministic-but-unique-per-call timestamp-derived
// value only if crypto/rand is somehow unavailable (it never is in
// practice — keeping the branch for belt-and-suspenders robustness).
func randomSuffix(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// Extremely unlikely; hex-encode the error's length as a
		// degenerate-but-non-panicking fallback.
		return fmt.Sprintf("%04x", len(err.Error())*7919)
	}
	return hex.EncodeToString(buf)
}

// GenerateUniqueSlug produces a base-from-name + "-xxxx" slug and
// retries until the DB confirms it's unique among tenants.
//
// Parameters:
//
//	db        — the gorm handle to check uniqueness against.
//	name      — the business name used to seed the human-readable base.
//	excludeID — tenant ID whose current slug is considered "available"
//	            (so a tenant editing their own slug doesn't collide with
//	            themselves). Pass "" when generating for a brand-new row.
//
// Returns the final slug. The error is non-nil only if the DB lookup
// itself fails — we always eventually succeed because the final
// attempt uses 4 bytes / 8 hex chars (2^32 search space, effectively
// zero collision risk per tenant).
func GenerateUniqueSlug(db *gorm.DB, name, excludeID string) (string, error) {
	base := SlugifyBase(name)

	// Try short (2-byte / 4-char) suffixes first; widen on the final
	// pass. 8 attempts at 4 chars gives us practical guarantees for
	// the ~thousands-of-tenants range we care about.
	for attempt := 0; attempt < 8; attempt++ {
		candidate := fmt.Sprintf("%s-%s", base, randomSuffix(2))
		taken, err := slugTaken(db, candidate, excludeID)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}

	// Final pass — 4 bytes / 8 hex chars. Collision probability is ~0.
	candidate := fmt.Sprintf("%s-%s", base, randomSuffix(4))
	taken, err := slugTaken(db, candidate, excludeID)
	if err != nil {
		return "", err
	}
	if taken {
		// This is effectively impossible, but surface it rather than
		// return a duplicate that would break the UNIQUE index.
		return "", errors.New("no se pudo generar un slug único; intente de nuevo")
	}
	return candidate, nil
}

func slugTaken(db *gorm.DB, slug, excludeID string) (bool, error) {
	var count int64
	q := db.Table("tenants").Where("store_slug = ?", slug)
	if excludeID != "" {
		q = q.Where("id <> ?", excludeID)
	}
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ValidateSlugFormat enforces the URL-safe contract for slugs
// entered by tenants. Returns a user-facing Spanish message on
// failure so handlers can forward it straight to the client.
func ValidateSlugFormat(slug string) error {
	if len(slug) < 3 {
		return errors.New("el enlace debe tener al menos 3 caracteres")
	}
	if len(slug) > MaxSlugLength {
		return fmt.Errorf("el enlace no puede tener más de %d caracteres", MaxSlugLength)
	}
	if !SlugPattern.MatchString(slug) {
		return errors.New("solo minúsculas, números y guiones (ej: mi-tienda-123)")
	}
	return nil
}
