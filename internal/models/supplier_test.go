package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSupplier_Fields(t *testing.T) {
	s := Supplier{
		TenantID:    "tenant-uuid",
		CompanyName: "Postobón S.A.",
		ContactName: "Pedro",
		Phone:       "3101234567",
		Emoji:       "🥤",
	}
	assert.Equal(t, "Postobón S.A.", s.CompanyName)
	assert.Equal(t, "Pedro", s.ContactName)
	assert.Equal(t, "3101234567", s.Phone)
	assert.Equal(t, "🥤", s.Emoji)
}
