package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestCreatePromotion_Validation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	r.POST("/promotions", func(c *gin.Context) {
		type Request struct {
			ProductUUID string  `json:"product_uuid" binding:"required"`
			PromoPrice  float64 `json:"promo_price"  binding:"required,gt=0"`
		}
		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": "ok"})
	})

	cases := []struct {
		name    string
		payload map[string]any
		code    int
	}{
		{
			name:    "missing product_uuid",
			payload: map[string]any{"promo_price": 1800},
			code:    http.StatusBadRequest,
		},
		{
			name:    "zero promo_price",
			payload: map[string]any{"product_uuid": "uuid", "promo_price": 0},
			code:    http.StatusBadRequest,
		},
		{
			name:    "valid",
			payload: map[string]any{"product_uuid": "uuid-1", "promo_price": 1800},
			code:    http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			body, _ := json.Marshal(tc.payload)
			req, _ := http.NewRequest("POST", "/promotions", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.code, w.Code, "case: %s, body: %s", tc.name, w.Body.String())
		})
	}
}
