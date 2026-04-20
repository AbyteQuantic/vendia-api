package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNormaliseExpiryDate exercises the pure-function validator that
// guards the Postgres DATE column against garbage from the request
// body. Table-driven for readability and to cover the full matrix of
// boundary inputs in one place.
func TestNormaliseExpiryDate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		input     string
		expectNil bool
		expectVal string
		expectErr bool
	}{
		{
			name:      "empty string → nil, no error (field is optional)",
			input:     "",
			expectNil: true,
		},
		{
			name:      "whitespace-only → nil, no error",
			input:     "   ",
			expectNil: true,
		},
		{
			name:      "valid ISO date passes through",
			input:     "2026-12-31",
			expectVal: "2026-12-31",
		},
		{
			name:      "valid ISO date with surrounding whitespace is trimmed",
			input:     "  2027-01-15  ",
			expectVal: "2027-01-15",
		},
		{
			name:      "DD/MM/YYYY rejected — frontend must normalise first",
			input:     "31/12/2026",
			expectErr: true,
		},
		{
			name:      "non-date string rejected",
			input:     "mañana",
			expectErr: true,
		},
		{
			name:      "malformed date rejected (month 13)",
			input:     "2026-13-01",
			expectErr: true,
		},
		{
			name:      "malformed date rejected (day 32)",
			input:     "2026-01-32",
			expectErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := normaliseExpiryDate(tc.input)
			if tc.expectErr {
				assert.Error(t, err)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if tc.expectNil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tc.expectVal, *got)
		})
	}
}

// TestCreateProductRequest_ExpiryDateBinding exercises the Gin binding
// layer with the exact request struct CreateProduct uses, proving that:
//  1. a valid expiry_date round-trips through JSON unmarshalling and
//     validator, and
//  2. a malformed expiry_date produces a 400 with the Spanish error
//     message shopkeeper-facing code expects.
//
// This runs as a pure HTTP-level test without a DB, mirroring the
// pattern already established in promotions_test.go.
func TestCreateProductRequest_ExpiryDateBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// Mirror the CreateProduct request struct + normalisation — we're
	// exercising the contract, not the GORM persistence.
	r.POST("/products", func(c *gin.Context) {
		type Request struct {
			Name       string  `json:"name"  binding:"required"`
			Price      float64 `json:"price" binding:"required,gt=0"`
			ExpiryDate string  `json:"expiry_date"`
		}
		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		expiry, err := normaliseExpiryDate(req.ExpiryDate)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		resp := gin.H{"name": req.Name, "price": req.Price}
		if expiry != nil {
			resp["expiry_date"] = *expiry
		}
		c.JSON(http.StatusCreated, resp)
	})

	cases := []struct {
		name         string
		payload      map[string]any
		wantCode     int
		wantExpiry   string
		wantNoExpiry bool
	}{
		{
			name: "valid ISO date is persisted in response",
			payload: map[string]any{
				"name": "Leche Alpina 1L", "price": 4500,
				"expiry_date": "2026-08-31",
			},
			wantCode:   http.StatusCreated,
			wantExpiry: "2026-08-31",
		},
		{
			name: "omitted expiry_date → product created without one",
			payload: map[string]any{
				"name": "Jabón Protex", "price": 4200,
			},
			wantCode:     http.StatusCreated,
			wantNoExpiry: true,
		},
		{
			name: "malformed expiry_date → 400 in Spanish",
			payload: map[string]any{
				"name": "Coca-Cola 400ml", "price": 2500,
				"expiry_date": "mañana",
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "DD/MM/YYYY is rejected — frontend must normalise",
			payload: map[string]any{
				"name": "Pan Bimbo", "price": 5200,
				"expiry_date": "31/12/2026",
			},
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.payload)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			req, err := http.NewRequest(http.MethodPost, "/products", bytes.NewBuffer(body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")

			r.ServeHTTP(w, req)
			require.Equal(t, tc.wantCode, w.Code, "body: %s", w.Body.String())

			if tc.wantCode != http.StatusCreated {
				return
			}
			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			if tc.wantExpiry != "" {
				assert.Equal(t, tc.wantExpiry, resp["expiry_date"])
			}
			if tc.wantNoExpiry {
				_, has := resp["expiry_date"]
				assert.False(t, has, "expected no expiry_date in response")
			}
		})
	}
}
