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

func TestCreateOrder_Validation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	r.POST("/orders", func(c *gin.Context) {
		type ItemRequest struct {
			ProductUUID string  `json:"product_uuid" binding:"required"`
			ProductName string  `json:"product_name" binding:"required"`
			Quantity    int     `json:"quantity"      binding:"required,min=1"`
			UnitPrice   float64 `json:"unit_price"    binding:"required,gt=0"`
		}
		type Request struct {
			Label string        `json:"label" binding:"required"`
			Items []ItemRequest `json:"items" binding:"required,min=1"`
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
			name:    "missing label",
			payload: map[string]any{"items": []map[string]any{{"product_uuid": "uuid", "product_name": "Test", "quantity": 1, "unit_price": 1000}}},
			code:    http.StatusBadRequest,
		},
		{
			name:    "empty items",
			payload: map[string]any{"label": "Mesa 1", "items": []map[string]any{}},
			code:    http.StatusBadRequest,
		},
		{
			name:    "no items key at all",
			payload: map[string]any{"label": "Mesa 1"},
			code:    http.StatusBadRequest,
		},
		{
			name: "valid order",
			payload: map[string]any{
				"label": "Mesa 1",
				"items": []map[string]any{
					{"product_uuid": "uuid-1", "product_name": "Hamburguesa", "quantity": 2, "unit_price": 12000},
				},
			},
			code: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			body, _ := json.Marshal(tc.payload)
			req, _ := http.NewRequest("POST", "/orders", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.code, w.Code, "case: %s, body: %s", tc.name, w.Body.String())
		})
	}
}

func TestUpdateOrderStatus_Validation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	r.PATCH("/orders/:uuid/status", func(c *gin.Context) {
		type Request struct {
			Status string `json:"status" binding:"required"`
		}
		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": "ok"})
	})

	// Missing status
	w := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{})
	req, _ := http.NewRequest("PATCH", "/orders/uuid-1/status", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Valid status
	w = httptest.NewRecorder()
	body, _ = json.Marshal(map[string]string{"status": "preparando"})
	req, _ = http.NewRequest("PATCH", "/orders/uuid-1/status", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}
