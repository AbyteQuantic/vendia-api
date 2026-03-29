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

func TestCreateWebOrder_Validation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	r.POST("/store/:slug/order", func(c *gin.Context) {
		type ItemReq struct {
			ProductUUID string `json:"product_uuid" binding:"required"`
			Quantity    int    `json:"quantity"      binding:"required,min=1"`
		}
		type Request struct {
			CustomerName    string    `json:"customer_name"     binding:"required"`
			CustomerPhone   string    `json:"customer_phone"    binding:"required"`
			DeliveryAddress string    `json:"delivery_address"  binding:"required"`
			Items           []ItemReq `json:"items"             binding:"required,min=1"`
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
			name: "missing customer_name",
			payload: map[string]any{
				"customer_phone":   "3001234567",
				"delivery_address": "Cra 5",
				"items":            []map[string]any{{"product_uuid": "uuid", "quantity": 1}},
			},
			code: http.StatusBadRequest,
		},
		{
			name: "missing items",
			payload: map[string]any{
				"customer_name":    "Carlos",
				"customer_phone":   "3001234567",
				"delivery_address": "Cra 5",
				"items":            []map[string]any{},
			},
			code: http.StatusBadRequest,
		},
		{
			name: "valid order",
			payload: map[string]any{
				"customer_name":    "Carlos",
				"customer_phone":   "3001234567",
				"delivery_address": "Cra 5 #10-20",
				"items":            []map[string]any{{"product_uuid": "uuid-1", "quantity": 2}},
			},
			code: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			body, _ := json.Marshal(tc.payload)
			req, _ := http.NewRequest("POST", "/store/don-pepe/order", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.code, w.Code, "case: %s, body: %s", tc.name, w.Body.String())
		})
	}
}

func TestFormatAmount(t *testing.T) {
	assert.Equal(t, "5000", formatAmount(5000))
	assert.Equal(t, "125000", formatAmount(125000))
	assert.Equal(t, "0", formatAmount(0))
}
