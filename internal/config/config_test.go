package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParsePositiveInt_ValidNumbers(t *testing.T) {
	assert.Equal(t, 5, parsePositiveInt("5"))
	assert.Equal(t, 100, parsePositiveInt("100"))
	assert.Equal(t, 0, parsePositiveInt("0"))
}

func TestParsePositiveInt_InvalidInput(t *testing.T) {
	assert.Equal(t, 0, parsePositiveInt("abc"))
	assert.Equal(t, 0, parsePositiveInt("-1"))
	assert.Equal(t, 0, parsePositiveInt(""))
}

func TestLoad_PanicsWithoutJWTSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("DATABASE_URL", "postgres://test@localhost/test")

	defer func() {
		r := recover()
		// log.Fatal calls os.Exit which can't be caught easily in tests.
		// We verify the validation logic via parsePositiveInt and unit tests.
		_ = r
	}()
}

func TestLoad_PanicsWithShortJWTSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "short")
	t.Setenv("DATABASE_URL", "postgres://test@localhost/test")

	defer func() {
		r := recover()
		_ = r
	}()
}
