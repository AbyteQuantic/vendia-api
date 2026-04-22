package services

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// slugSuffixRE matches a valid base + "-xxxx[xxxx]" suffix.
var slugSuffixRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*-[0-9a-f]{4,8}$`)

func TestSlugifyBase(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// The canonical ticket example.
		{"Tienda Don Pepe", "tienda-don-pepe"},
		// Diacritic folding is the whole point of NFD normalization.
		{"Tienda Don Pepé", "tienda-don-pepe"},
		{"Café & Más", "cafe-mas"},
		// Mixed case + punctuation squashes to a single dash.
		{"  Hola   Mundo!!  ", "hola-mundo"},
		// ñ is treated as a non-ASCII letter → dropped after folding.
		// (Go's NFD doesn't split ñ; it becomes just "n".)
		{"Piñata", "pinata"},
		// Empty/whitespace-only falls back to the safe default so we
		// never produce a slug that's "-abcd".
		{"", "tienda"},
		{"   ", "tienda"},
		// Pure non-ASCII symbols should also fall back.
		{"!!!", "tienda"},
		// Dashes at the edges are trimmed.
		{"--foo--", "foo"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := SlugifyBase(tc.in)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSlugifyBase_RespectsMaxLength(t *testing.T) {
	// Build a name that would produce a base longer than the reserved
	// budget (MaxSlugLength - 5 for the "-xxxx" suffix). Expect the
	// truncation to land on or before the budget and never end in "-".
	long := "tienda super ultra mega extremadamente larga del barrio"
	got := SlugifyBase(long)
	assert.LessOrEqual(t, len(got), MaxSlugLength-5)
	assert.NotEqual(t, byte('-'), got[len(got)-1])
}

func TestValidateSlugFormat(t *testing.T) {
	cases := []struct {
		name    string
		slug    string
		wantErr bool
	}{
		{"valid canonical", "tienda-don-pepe-a4x9", false},
		{"valid digits", "tienda123", false},
		{"valid hyphen", "a-b", false},
		{"too short", "ab", true},
		{"uppercase rejected", "Tienda-Don", true},
		{"space rejected", "tienda don", true},
		{"leading dash rejected", "-tienda", true},
		{"trailing dash rejected", "tienda-", true},
		{"double dash rejected", "tienda--pepe", true},
		{"accent rejected (normalize first)", "café", true},
		{"underscore rejected", "mi_tienda", true},
		{"slash rejected", "mi/tienda", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSlugFormat(tc.slug)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRandomSuffixShape(t *testing.T) {
	// The suffix is the uniqueness-guarantee bit; make sure it's
	// lowercase hex of the expected length and actually varies across
	// calls (so we never ship a deterministic "suffix" that defeats
	// the collision-avoidance contract).
	hexRE := regexp.MustCompile(`^[0-9a-f]+$`)
	a := randomSuffix(2)
	b := randomSuffix(2)
	assert.Len(t, a, 4)
	assert.Len(t, b, 4)
	assert.Regexp(t, hexRE, a)
	assert.Regexp(t, hexRE, b)
	// Not a guarantee — but across 2^16 space, flaky probability is
	// 1/65536. Fine as a smoke check and catches an all-zeros regression.
	assert.NotEqual(t, a, b, "two consecutive random suffixes should differ")
}

// TestGeneratedSlugMatchesContract uses the public SlugPattern to
// verify the full "base + suffix" output satisfies the URL-safe
// contract handlers enforce. No DB required.
func TestGeneratedSlugMatchesContract(t *testing.T) {
	for _, name := range []string{"Tienda Don Pepe", "Café & Más", ""} {
		base := SlugifyBase(name)
		candidate := base + "-" + randomSuffix(2)
		assert.Regexp(t, slugSuffixRE, candidate,
			"generated slug %q does not satisfy URL-safe contract", candidate)
		assert.Regexp(t, SlugPattern, candidate,
			"generated slug %q does not satisfy SlugPattern", candidate)
	}
}
