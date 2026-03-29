package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRockolaSuggestion_Fields(t *testing.T) {
	song := RockolaSuggestion{
		TenantID:    "tenant-uuid",
		TrackName:   "Waka Waka",
		ArtistName:  "Shakira",
		ArtworkURL:  "https://example.com/art.jpg",
		Status:      "pending",
		SuggestedBy: "Carlos",
	}

	assert.Equal(t, "Waka Waka", song.TrackName)
	assert.Equal(t, "Shakira", song.ArtistName)
	assert.Equal(t, "pending", song.Status)
	assert.Equal(t, "Carlos", song.SuggestedBy)
}
