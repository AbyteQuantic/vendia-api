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

func TestCreateSupplier_Validation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	r.POST("/suppliers", func(c *gin.Context) {
		type Request struct {
			CompanyName string `json:"company_name" binding:"required"`
			Phone       string `json:"phone"        binding:"required"`
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
		payload map[string]string
		code    int
	}{
		{
			name:    "missing company_name",
			payload: map[string]string{"phone": "3101234567"},
			code:    http.StatusBadRequest,
		},
		{
			name:    "missing phone",
			payload: map[string]string{"company_name": "Postobón"},
			code:    http.StatusBadRequest,
		},
		{
			name:    "valid",
			payload: map[string]string{"company_name": "Postobón", "phone": "3101234567"},
			code:    http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			body, _ := json.Marshal(tc.payload)
			req, _ := http.NewRequest("POST", "/suppliers", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.code, w.Code, "case: %s, body: %s", tc.name, w.Body.String())
		})
	}
}
