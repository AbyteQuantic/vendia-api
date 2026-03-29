package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewITunesService(t *testing.T) {
	svc := NewITunesService()
	assert.NotNil(t, svc)
	assert.NotNil(t, svc.client)
}
