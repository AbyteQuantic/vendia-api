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

func TestCreateRecipe_Validation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	r.POST("/recipes", func(c *gin.Context) {
		type IngredientInput struct {
			ProductUUID string  `json:"product_uuid" binding:"required"`
			ProductName string  `json:"product_name" binding:"required"`
			Quantity    float64 `json:"quantity"      binding:"required,gt=0"`
			UnitCost    float64 `json:"unit_cost"     binding:"required,gt=0"`
		}
		type Request struct {
			ProductName string            `json:"product_name" binding:"required"`
			SalePrice   float64           `json:"sale_price"   binding:"required,gt=0"`
			Ingredients []IngredientInput `json:"ingredients"  binding:"required,min=1"`
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
			name:    "missing product_name",
			payload: map[string]any{"sale_price": 5000, "ingredients": []map[string]any{{"product_uuid": "u", "product_name": "Pan", "quantity": 1, "unit_cost": 500}}},
			code:    http.StatusBadRequest,
		},
		{
			name:    "zero sale_price",
			payload: map[string]any{"product_name": "Perro", "sale_price": 0, "ingredients": []map[string]any{{"product_uuid": "u", "product_name": "Pan", "quantity": 1, "unit_cost": 500}}},
			code:    http.StatusBadRequest,
		},
		{
			name:    "empty ingredients",
			payload: map[string]any{"product_name": "Perro", "sale_price": 5000, "ingredients": []map[string]any{}},
			code:    http.StatusBadRequest,
		},
		{
			name: "valid recipe",
			payload: map[string]any{
				"product_name": "Perro Caliente",
				"sale_price":   5000,
				"ingredients": []map[string]any{
					{"product_uuid": "uuid-1", "product_name": "Pan", "quantity": 1, "unit_cost": 500},
				},
			},
			code: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			body, _ := json.Marshal(tc.payload)
			req, _ := http.NewRequest("POST", "/recipes", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.code, w.Code, "case: %s, body: %s", tc.name, w.Body.String())
		})
	}
}
