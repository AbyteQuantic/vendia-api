// Spec: specs/015-ia-foto-timeouts/spec.md
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func init() { gin.SetMode(gin.TestMode) }

// TestAIImageOperationTimeout_IsOrdered pins the timeout budget.
// Spec FR-01 / D1: the backend context (~110s) must be generous
// enough for download + Gemini (~27s) + upload, and must stay below
// the frontend's per-request receiveTimeout (~140s).
func TestAIImageOperationTimeout_IsOrdered(t *testing.T) {
	assert.Equal(t, 110*time.Second, aiImageOperationTimeout,
		"AI image operation budget must be 110s — FR-01")
	assert.Less(t, aiImageOperationTimeout, 140*time.Second,
		"backend ctx must stay below the frontend receiveTimeout (~140s) — D1")
	assert.Greater(t, aiImageOperationTimeout, 30*time.Second,
		"110s must beat the old 30s budget that caused the bug")
}

// TestRespondAIImageError_TimeoutFailsCleanly verifies FR-03: when an
// AI photo operation runs past its context, the handler fails fast
// with a clear Spanish message and never leaks "context deadline
// exceeded" to the shopkeeper.
func TestRespondAIImageError_TimeoutFailsCleanly(t *testing.T) {
	cases := []struct {
		name   string
		ctxErr error // error the context carries (nil = live ctx)
		err    error // error returned by the AI/storage call
	}{
		{"deadline exceeded error", nil, context.DeadlineExceeded},
		{"canceled error", nil, context.Canceled},
		{"wrapped deadline error", nil, fmt.Errorf("gemini enhance request failed: %w", context.DeadlineExceeded)},
		{"context already expired", context.DeadlineExceeded, errors.New("connection reset")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			ctx := context.Background()
			if tc.ctxErr != nil {
				// An already-expired context.
				expired, cancel := context.WithTimeout(ctx, time.Nanosecond)
				defer cancel()
				time.Sleep(time.Millisecond)
				ctx = expired
			}

			respondAIImageError(c, ctx, "error al mejorar foto", tc.err)

			assert.Equal(t, http.StatusGatewayTimeout, w.Code,
				"a timeout/cancellation must map to 504, fast")

			var body map[string]string
			assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, aiTimeoutMessage, body["error"],
				"timeout must return the clean Spanish message")
			assert.NotContains(t, body["error"], "context deadline",
				"raw Go error must never leak to the user — Art. V")
			assert.NotContains(t, body["error"], "canceled")
		})
	}
}

// TestRespondAIImageError_NonTimeoutKeepsPrefix verifies that a real
// (non-timeout) failure still surfaces with its Spanish prefix and a
// 500 — the timeout path must not swallow genuine errors.
func TestRespondAIImageError_NonTimeoutKeepsPrefix(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	respondAIImageError(c, context.Background(), "error al guardar foto mejorada",
		errors.New("R2 upload failed: access denied"))

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var body map[string]string
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Contains(t, body["error"], "error al guardar foto mejorada",
		"non-timeout errors keep their Spanish prefix")
}

// TestAITimeoutMessage_IsSpanish guards Constitution Art. V.
func TestAITimeoutMessage_IsSpanish(t *testing.T) {
	assert.NotEmpty(t, aiTimeoutMessage)
	assert.NotContains(t, aiTimeoutMessage, "context")
	assert.NotContains(t, aiTimeoutMessage, "deadline")
	assert.Contains(t, aiTimeoutMessage, "IA")
}
