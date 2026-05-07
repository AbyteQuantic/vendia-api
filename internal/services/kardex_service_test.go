package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestImageIdempotencyKey_DeterministicForSameInput(t *testing.T) {
	data := []byte("test image data for invoice scan")
	key1 := ImageIdempotencyKey(data)
	key2 := ImageIdempotencyKey(data)
	assert.Equal(t, key1, key2, "same input must produce same key")
	assert.Contains(t, key1, "img:", "key must have img: prefix")
}

func TestImageIdempotencyKey_DifferentForDifferentInput(t *testing.T) {
	data1 := []byte("invoice photo 1")
	data2 := []byte("invoice photo 2")
	assert.NotEqual(t, ImageIdempotencyKey(data1), ImageIdempotencyKey(data2))
}

func TestImageIdempotencyKey_HandlesSmallInput(t *testing.T) {
	data := []byte("hi")
	key := ImageIdempotencyKey(data)
	assert.NotEmpty(t, key)
}

func TestImageIdempotencyKey_TruncatesAt2KB(t *testing.T) {
	// Two images that differ only after 2KB should produce the same key
	base := make([]byte, 3000)
	for i := range base {
		base[i] = 'A'
	}
	copy1 := make([]byte, 3000)
	copy(copy1, base)
	copy1[2500] = 'Z' // differs after 2KB

	assert.Equal(t, ImageIdempotencyKey(base), ImageIdempotencyKey(copy1),
		"changes after 2KB should not affect the key")
}
