package services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewGeminiService_Defaults(t *testing.T) {
	svc := NewGeminiService("test-key", "", "", 0)
	assert.NotNil(t, svc)
	assert.Equal(t, "gemini-2.0-flash", svc.model)
	assert.Equal(t, "gemini-2.5-flash-image", svc.imageModel)
	assert.Equal(t, 30*time.Second, svc.timeout)
	assert.Equal(t, "test-key", svc.apiKey)
}

func TestNewGeminiService_CustomModel(t *testing.T) {
	svc := NewGeminiService("key", "gemini-pro", "custom-image", 60*time.Second)
	assert.Equal(t, "gemini-pro", svc.model)
	assert.Equal(t, "custom-image", svc.imageModel)
	assert.Equal(t, 60*time.Second, svc.timeout)
}
