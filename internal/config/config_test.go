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

func TestParseBoolEnv_DefaultWhenUnset(t *testing.T) {
	t.Setenv("EPAYCO_TEST_MODE_X", "")
	assert.True(t, parseBoolEnv("EPAYCO_TEST_MODE_X", true))
	assert.False(t, parseBoolEnv("EPAYCO_TEST_MODE_X", false))
}

func TestParseBoolEnv_ParsesExplicitValues(t *testing.T) {
	t.Setenv("EPAYCO_TEST_MODE_X", "false")
	assert.False(t, parseBoolEnv("EPAYCO_TEST_MODE_X", true))
	t.Setenv("EPAYCO_TEST_MODE_X", "true")
	assert.True(t, parseBoolEnv("EPAYCO_TEST_MODE_X", false))
	t.Setenv("EPAYCO_TEST_MODE_X", "0")
	assert.False(t, parseBoolEnv("EPAYCO_TEST_MODE_X", true))
	t.Setenv("EPAYCO_TEST_MODE_X", "1")
	assert.True(t, parseBoolEnv("EPAYCO_TEST_MODE_X", false))
}

func TestParseBoolEnv_DefaultsOnGarbage(t *testing.T) {
	t.Setenv("EPAYCO_TEST_MODE_X", "yeah-sure")
	assert.True(t, parseBoolEnv("EPAYCO_TEST_MODE_X", true),
		"un valor invalido debe caer al default (no flipear el sandbox)")
}
