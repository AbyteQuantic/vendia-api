// Spec: specs/070-galeria-multimedia-producto/spec.md
package services

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseYouTubeID(t *testing.T) {
	ok := map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":         "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ":                        "dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ":          "dQw4w9WgXcQ",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ&feature=x": "dQw4w9WgXcQ",
	}
	for link, id := range ok {
		got, err := ParseYouTubeID(link)
		require.NoError(t, err, link)
		assert.Equal(t, id, got, link)
	}

	bad := []string{
		"", "https://vimeo.com/123", "https://www.youtube.com/watch?v=corto",
		"not a url at all ::", "https://youtu.be/",
	}
	for _, link := range bad {
		_, err := ParseYouTubeID(link)
		assert.Error(t, err, link)
	}
}

func TestYouTubeThumbnailAndCanonical(t *testing.T) {
	assert.Equal(t, "https://i.ytimg.com/vi/abc/hqdefault.jpg", YouTubeThumbnail("abc"))
	assert.Equal(t, "https://www.youtube.com/watch?v=abc", YouTubeCanonicalURL("abc"))
}

// buildMp4 arma un MP4 mínimo con moov>mvhd (version 0) de la duración dada.
func buildMp4(timescale uint32, duration uint32) []byte {
	mvhdBody := make([]byte, 100) // version+flags(4)+created(4)+modified(4)+ts(4)+dur(4)+resto
	// version 0 (mvhdBody[0]=0 ya). timescale en [12:16], duration en [16:20].
	binary.BigEndian.PutUint32(mvhdBody[12:16], timescale)
	binary.BigEndian.PutUint32(mvhdBody[16:20], duration)
	mvhd := box("mvhd", mvhdBody)
	moov := box("moov", mvhd)
	ftyp := box("ftyp", make([]byte, 8))
	return append(ftyp, moov...)
}

func box(typ string, body []byte) []byte {
	out := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(body)))
	copy(out[4:8], typ)
	copy(out[8:], body)
	return out
}

func TestMp4DurationSeconds(t *testing.T) {
	// 25s exactos: timescale 1000, duration 25000.
	d, err := Mp4DurationSeconds(buildMp4(1000, 25000))
	require.NoError(t, err)
	assert.Equal(t, 25, d)

	// 30s: timescale 600, duration 18000.
	d2, err := Mp4DurationSeconds(buildMp4(600, 18000))
	require.NoError(t, err)
	assert.Equal(t, 30, d2)

	// Sin moov → ErrNoDuration (el caller usa el tope de peso).
	_, err = Mp4DurationSeconds([]byte("no es un mp4"))
	assert.ErrorIs(t, err, ErrNoDuration)
}
