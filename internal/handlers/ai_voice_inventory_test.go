package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildAudioForm assembles a multipart/form-data body with the
// audio_file field. Returns body + content-type header.
func buildAudioForm(t *testing.T, fieldName, fileName, mimeType string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, fileName))
	if mimeType != "" {
		h.Set("Content-Type", mimeType)
	}
	part, err := w.CreatePart(h)
	require.NoError(t, err)
	_, err = io.Copy(part, bytes.NewReader(content))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return body, w.FormDataContentType()
}

func postAudio(t *testing.T, r *gin.Engine, body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/ai/voice-inventory", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func mountVoiceHandler(svc *services.GeminiService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/ai/voice-inventory", handlers.VoiceInventory(svc))
	return r
}

func TestVoiceInventory_RejectsWhenServiceUnconfigured(t *testing.T) {
	r := mountVoiceHandler(nil)
	body, ct := buildAudioForm(t, "audio_file", "clip.m4a", "audio/m4a",
		[]byte("fake audio bytes"))

	w := postAudio(t, r, body, ct)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "servicio de IA no configurado")
}

func TestVoiceInventory_RejectsMissingAudioField(t *testing.T) {
	r := mountVoiceHandler(&services.GeminiService{})
	// Build a multipart body with the wrong field name
	body, ct := buildAudioForm(t, "wrong_field", "clip.m4a", "audio/m4a",
		[]byte("x"))

	w := postAudio(t, r, body, ct)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "audio_file")
}

func TestVoiceInventory_RejectsEmptyFile(t *testing.T) {
	r := mountVoiceHandler(&services.GeminiService{})
	body, ct := buildAudioForm(t, "audio_file", "empty.m4a", "audio/m4a",
		[]byte{})

	w := postAudio(t, r, body, ct)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "vacío")
}

func TestVoiceInventory_RejectsOversizedFile(t *testing.T) {
	r := mountVoiceHandler(&services.GeminiService{})
	oversized := make([]byte, (10<<20)+1) // 10 MiB + 1 byte

	body, ct := buildAudioForm(t, "audio_file", "big.m4a", "audio/m4a",
		oversized)
	w := postAudio(t, r, body, ct)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "10MB")
}

func TestVoiceInventory_RejectsUnsupportedMimeType(t *testing.T) {
	r := mountVoiceHandler(&services.GeminiService{})
	body, ct := buildAudioForm(t, "audio_file", "clip.flac", "audio/flac",
		[]byte("fake bytes"))

	w := postAudio(t, r, body, ct)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var body2 map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body2))
	assert.Equal(t, "unsupported_audio_type", body2["error_code"])
	assert.Equal(t, "audio/flac", body2["received"])
}

func TestVoiceInventory_AcceptsCommonAudioMimeTypes(t *testing.T) {
	r := mountVoiceHandler(&services.GeminiService{})
	// These mimes pass the validation gate. The request then fails
	// downstream because GeminiService can't actually call out in a
	// test (empty apiKey), but the 422 from the model is acceptable —
	// the point of this test is that the mime-type gate doesn't
	// incorrectly reject formats the tendero's phone might record.
	for _, mt := range []string{
		"audio/m4a", "audio/mp4", "audio/webm", "audio/mp3", "audio/ogg",
	} {
		t.Run(mt, func(t *testing.T) {
			body, ct := buildAudioForm(t, "audio_file", "clip", mt,
				[]byte("dummy-non-empty"))

			w := postAudio(t, r, body, ct)
			// Must NOT be the 400 "unsupported_audio_type" path.
			assert.NotEqual(t, http.StatusBadRequest, w.Code,
				"mime %q was rejected by the gate", mt)
		})
	}
}
