// Spec: specs/105-hito-restaurante-comandas/spec.md — F1.
package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupOrders105DB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.OrderTicket{}, &models.OrderItem{}, &models.Product{},
	))
	return db
}

func mountOrders105(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/orders", handlers.CreateOrder(db))
	r.PATCH("/orders/:uuid/status", handlers.UpdateOrderStatus(db, nil))
	r.POST("/orders/:uuid/close", handlers.CloseOrder(db))
	return r
}

func do105(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// crea un ticket con un producto que tiene duration_min en BD.
func seedOrder105(t *testing.T, db *gorm.DB, r *gin.Engine, tenantID string, saleUUID string) (orderID string) {
	t.Helper()
	dur := 20
	prod := models.Product{TenantID: tenantID, Name: "Empanada", Price: 3500, DurationMin: &dur}
	require.NoError(t, db.Create(&prod).Error)

	payload := map[string]any{
		"label": "Mostrador 1",
		"items": []map[string]any{{
			"product_uuid": prod.ID, "product_name": "Empanada",
			"quantity": 3, "unit_price": 3500, "notes": "sin cebolla",
		}},
		"type": "turno",
	}
	if saleUUID != "" {
		payload["sale_uuid"] = saleUUID
	}
	w := do105(t, r, http.MethodPost, "/orders", payload)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var resp struct {
		Data models.OrderTicket `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp.Data.ID
}

// AC-F1: el snapshot de duration_min sale de la BD (server-side) y las notas
// del cliente viajan al ítem de la comanda.
func TestSpec105_CreateOrder_SnapshotDurationYNotas(t *testing.T) {
	db := setupOrders105DB(t)
	r := mountOrders105(db, "t1")
	orderID := seedOrder105(t, db, r, "t1", "")

	var item models.OrderItem
	require.NoError(t, db.First(&item, "order_uuid = ?", orderID).Error)
	require.NotNil(t, item.DurationMin, "duration_min debe congelarse en el ítem")
	assert.Equal(t, 20, *item.DurationMin)
	assert.Equal(t, "sin cebolla", item.Notes)
}

// AC-F1: listo→entregado→cobrado funciona con timestamps; listo→cobrado
// directo se conserva (retro-compat mesas).
func TestSpec105_Transiciones_EntregadoYRetroCompat(t *testing.T) {
	db := setupOrders105DB(t)
	r := mountOrders105(db, "t1")

	// Camino nuevo: nuevo→preparando→listo→entregado.
	id := seedOrder105(t, db, r, "t1", "")
	for _, st := range []string{"preparando", "listo", "entregado"} {
		w := do105(t, r, http.MethodPatch, fmt.Sprintf("/orders/%s/status", id), map[string]any{"status": st})
		require.Equal(t, http.StatusOK, w.Code, "→%s: %s", st, w.Body.String())
	}
	var row models.OrderTicket
	require.NoError(t, db.First(&row, "id = ?", id).Error)
	assert.Equal(t, models.OrderStatusEntregado, row.Status)
	assert.NotNil(t, row.PreparandoAt)
	assert.NotNil(t, row.ListoAt)
	assert.NotNil(t, row.EntregadoAt)

	// Retro-compat: listo→cobrado directo sigue permitido.
	id2 := seedOrder105(t, db, r, "t1", "")
	for _, st := range []string{"preparando", "listo", "cobrado"} {
		w := do105(t, r, http.MethodPatch, fmt.Sprintf("/orders/%s/status", id2), map[string]any{"status": st})
		require.Equal(t, http.StatusOK, w.Code, "→%s: %s", st, w.Body.String())
	}
}

// AC-F1: el segundo PATCH a 'entregado' es idempotente (mesero y cajero
// pueden marcar a la vez) — 200, no 400.
func TestSpec105_EntregadoIdempotente(t *testing.T) {
	db := setupOrders105DB(t)
	r := mountOrders105(db, "t1")
	id := seedOrder105(t, db, r, "t1", "")
	for _, st := range []string{"preparando", "listo", "entregado"} {
		do105(t, r, http.MethodPatch, fmt.Sprintf("/orders/%s/status", id), map[string]any{"status": st})
	}

	w := do105(t, r, http.MethodPatch, fmt.Sprintf("/orders/%s/status", id), map[string]any{"status": "entregado"})
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "ya estaba entregado")
}

// AC-F1 (riesgo crítico del concilio): un ticket PREPAGO (sale_uuid del cobro
// POS) jamás pasa por CloseOrder — 409, sin segunda venta ni doble stock.
func TestSpec105_Prepago_CloseOrderRechaza(t *testing.T) {
	db := setupOrders105DB(t)
	r := mountOrders105(db, "t1")
	saleUUID := uuid.NewString()
	id := seedOrder105(t, db, r, "t1", saleUUID)

	var row models.OrderTicket
	require.NoError(t, db.First(&row, "id = ?", id).Error)
	require.NotNil(t, row.PaidAt, "el ticket con sale_uuid nace pagado")
	require.NotNil(t, row.SaleUUID)
	assert.Equal(t, saleUUID, *row.SaleUUID)

	w := do105(t, r, http.MethodPost, fmt.Sprintf("/orders/%s/close", id), map[string]any{"payment_method": "efectivo"})
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "already_paid")
}
