package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewOpenFoodFactsService(t *testing.T) {
	svc := NewOpenFoodFactsService()
	assert.NotNil(t, svc)
	assert.Equal(t, "https://world.openfoodfacts.org/api/v2/product", svc.baseURL)
	assert.NotNil(t, svc.client)
}
