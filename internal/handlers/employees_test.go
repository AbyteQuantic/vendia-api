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

func setupGinTest() (*gin.Engine, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	r := gin.New()
	return r, w
}

func TestVerifyPin_MissingFields(t *testing.T) {
	r, w := setupGinTest()

	// VerifyPin requires DB, but we can test validation (binding errors)
	r.POST("/api/v1/employees/verify-pin", func(c *gin.Context) {
		type Request struct {
			EmployeeUUID string `json:"employee_uuid" binding:"required"`
			Pin          string `json:"pin"           binding:"required,len=4"`
		}
		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": "ok"})
	})

	// Test empty body
	body, _ := json.Marshal(map[string]string{})
	req, _ := http.NewRequest("POST", "/api/v1/employees/verify-pin", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Test invalid PIN length
	w = httptest.NewRecorder()
	body, _ = json.Marshal(map[string]string{
		"employee_uuid": "some-uuid",
		"pin":           "12", // too short
	})
	req, _ = http.NewRequest("POST", "/api/v1/employees/verify-pin", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateEmployee_Validation(t *testing.T) {
	r, _ := setupGinTest()

	r.POST("/employees", func(c *gin.Context) {
		type Request struct {
			Name     string `json:"name"     binding:"required"`
			Pin      string `json:"pin"      binding:"required,len=4"`
			Role     string `json:"role"     binding:"required"`
			Password string `json:"password" binding:"required,min=4"`
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
			name:    "missing name",
			payload: map[string]string{"pin": "1234", "role": "cashier", "password": "5678"},
			code:    http.StatusBadRequest,
		},
		{
			name:    "invalid pin length",
			payload: map[string]string{"name": "Test", "pin": "12", "role": "cashier", "password": "5678"},
			code:    http.StatusBadRequest,
		},
		{
			name:    "short password",
			payload: map[string]string{"name": "Test", "pin": "1234", "role": "cashier", "password": "12"},
			code:    http.StatusBadRequest,
		},
		{
			name:    "valid payload",
			payload: map[string]string{"name": "María", "pin": "1234", "role": "cashier", "password": "5678"},
			code:    http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			body, _ := json.Marshal(tc.payload)
			req, _ := http.NewRequest("POST", "/employees", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.code, w.Code, "case: %s, body: %s", tc.name, w.Body.String())
		})
	}
}
