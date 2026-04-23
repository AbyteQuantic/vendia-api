package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
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

// TestCreatePromotion_ComboSurfacesDriverError exercises the real
// CreatePromotion handler against an in-memory SQLite DB where the
// `promotions` table exists but `promotion_items` does not. The
// transaction must fail inside the items loop and we assert the 500
// body carries BOTH the human-readable "error" AND the raw "detail"
// so the tendero (or Ops) can diagnose from the toast instead of
// staring at a generic "error al crear promoción combo".
//
// This locks in the transparency contract introduced to debug the
// reported "Error al guardar: AppError(AppErrorType.server); error al
// crear promoción combo" failure from the Promo Builder wizard.
func TestCreatePromotion_ComboSurfacesDriverError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Migrate everything the happy path touches EXCEPT
	// promotion_items — that's how we force a failure inside the
	// transaction after the parent Promotion has been created.
	if err := db.AutoMigrate(&models.Product{}, &models.Promotion{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tenantID := "tenant-1"
	prod := models.Product{
		BaseModel: models.BaseModel{ID: "prod-1"},
		TenantID:  tenantID,
		Name:      "Coca Cola",
		Price:     3500,
	}
	if err := db.Create(&prod).Error; err != nil {
		t.Fatalf("seed product: %v", err)
	}

	r := gin.New()
	r.POST("/promotions", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		CreatePromotion(db)(c)
	})

	payload := map[string]any{
		"id":         "promo-1",
		"name":       "Combo Desayuno",
		"promo_type": "combo",
		"items": []map[string]any{
			{"product_id": prod.ID, "quantity": 1, "promo_price": 3000},
		},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", "/promotions", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code, "body=%s", w.Body.String())

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	assert.Equal(t, "error al crear promoción combo", resp["error"],
		"user-facing message must stay stable so toast copy doesn't break")
	detail, _ := resp["detail"].(string)
	assert.NotEmpty(t, detail, "detail should carry the raw driver error")
	assert.Contains(t, detail, "promotion_items",
		"detail should name the missing relation — that's the whole point of surfacing it")
	assert.Equal(t, "promotion_item", resp["step"],
		"step tag lets us tell promotion vs promotion_item failures apart")
}
