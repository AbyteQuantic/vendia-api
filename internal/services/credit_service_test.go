package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrCreditNotFound_IsError(t *testing.T) {
	assert.Error(t, ErrCreditNotFound)
	assert.Error(t, ErrPaymentExceeds)
	assert.Error(t, ErrCreditAlreadyPaid)
}
