// Spec: specs/105-hito-restaurante-comandas/spec.md — F2 (KDS + entregado visible + push + sweep).
package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services/push"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupOrdersF2DB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.OrderTicket{}, &models.OrderItem{}, &models.Product{},
		&models.Tenant{}, &models.TenantSubscription{},
		&models.DeviceToken{}, &models.PartialPayment{},
	))
	// Notifications a mano (gen_random_uuid Postgres-only).
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS notifications (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			tenant_id TEXT NOT NULL,
			title TEXT NOT NULL,
			body TEXT DEFAULT '',
			type TEXT DEFAULT 'info',
			is_read INTEGER DEFAULT 0,
			deep_link TEXT,
			pushed_at DATETIME,
			dedup_key TEXT
		)
	`).Error)
	return db
}

func mountOrdersF2(db *gorm.DB, tenantID string, dispatcher *push.Dispatcher) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.POST("/orders", handlers.CreateOrder(db))
	r.GET("/orders", handlers.ListOrders(db))
	r.GET("/orders/open-accounts", handlers.OpenAccounts(db))
	r.PATCH("/orders/:uuid/status", handlers.UpdateOrderStatus(db, dispatcher))
	return r
}

// seedTicketF2 inserta un ticket directo en BD con estado y edad arbitrarios.
func seedTicketF2(t *testing.T, db *gorm.DB, tenantID, label string, status models.OrderStatus, paid bool, listoAgo time.Duration) models.OrderTicket {
	t.Helper()
	ticket := models.OrderTicket{
		TenantID: tenantID,
		Label:    label,
		Status:   status,
		Type:     models.OrderTypeMesa,
		Total:    10000,
		Items: []models.OrderItem{{
			ProductUUID: uuid.NewString(), ProductName: "Empanada",
			Quantity: 2, UnitPrice: 5000,
		}},
	}
	if paid {
		now := time.Now()
		sale := uuid.NewString()
		ticket.PaidAt = &now
		ticket.SaleUUID = &sale
		ticket.Type = models.OrderTypeTurno
	}
	if listoAgo > 0 {
		at := time.Now().Add(-listoAgo)
		ticket.ListoAt = &at
	}
	require.NoError(t, db.Create(&ticket).Error)
	return ticket
}

// AC-F2: una cuenta de mesa ENTREGADA y sin pagar sigue apareciendo en
// open-accounts — el mesero entrega, el cajero todavía debe cobrar.
// Los tickets PREPAGO (paid_at) jamás aparecen: no hay dinero pendiente.
func TestSpec105F2_OpenAccounts_EntregadoSinPagarVisible(t *testing.T) {
	db := setupOrdersF2DB(t)
	r := mountOrdersF2(db, "t1", nil)

	entregadoSinPagar := seedTicketF2(t, db, "t1", "Mesa 4", models.OrderStatusEntregado, false, 0)
	prepagoListo := seedTicketF2(t, db, "t1", "Pedido 12", models.OrderStatusListo, true, 0)

	w := do105(t, r, http.MethodGet, "/orders/open-accounts", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data []models.OrderTicket `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	ids := make(map[string]bool, len(resp.Data))
	for _, o := range resp.Data {
		ids[o.ID] = true
	}
	assert.True(t, ids[entregadoSinPagar.ID], "mesa entregada sin pagar DEBE seguir abierta para cobro")
	assert.False(t, ids[prepagoListo.ID], "ticket prepago no es cuenta abierta (ya hay venta)")
}

// AC-F2: el KDS pide varios estados en un solo GET — ?status=nuevo,preparando,listo.
func TestSpec105F2_ListOrders_MultiStatus(t *testing.T) {
	db := setupOrdersF2DB(t)
	r := mountOrdersF2(db, "t1", nil)

	nuevo := seedTicketF2(t, db, "t1", "Mesa 1", models.OrderStatusNuevo, false, 0)
	preparando := seedTicketF2(t, db, "t1", "Mesa 2", models.OrderStatusPreparando, false, 0)
	listo := seedTicketF2(t, db, "t1", "Mesa 3", models.OrderStatusListo, false, 0)
	cobrado := seedTicketF2(t, db, "t1", "Mesa 5", models.OrderStatusCobrado, false, 0)

	w := do105(t, r, http.MethodGet, "/orders?status=nuevo,preparando,listo", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data []models.OrderTicket `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	ids := make(map[string]bool, len(resp.Data))
	for _, o := range resp.Data {
		ids[o.ID] = true
	}
	assert.True(t, ids[nuevo.ID])
	assert.True(t, ids[preparando.ID])
	assert.True(t, ids[listo.ID])
	assert.False(t, ids[cobrado.ID], "cobrado no es estado de cocina")

	// El filtro de un solo estado se conserva (retro-compat).
	w = do105(t, r, http.MethodGet, "/orders?status=listo", nil)
	require.Equal(t, http.StatusOK, w.Code)
	resp.Data = nil
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, listo.ID, resp.Data[0].ID)
}

// AC-F2: al pasar a 'listo' se despacha push F038 al tenant con dedup por
// ticket. Sin dispatcher (nil) el PATCH sigue funcionando.
func TestSpec105F2_PushAlPasarAListo(t *testing.T) {
	db := setupOrdersF2DB(t)

	// Tenant + token de dispositivo activos para que el dispatcher envíe.
	tenant := models.Tenant{BusinessName: "Restaurante F2", Phone: "3000000000"}
	require.NoError(t, db.Create(&tenant).Error)
	token := models.DeviceToken{TenantID: tenant.ID, UserID: uuid.NewString(), Token: "fcm-token-1", Platform: "android"}
	require.NoError(t, db.Create(&token).Error)

	fake := &push.FakeSender{}
	dispatcher := push.NewDispatcher(fake)
	r := mountOrdersF2(db, tenant.ID, dispatcher)

	ticket := seedTicketF2(t, db, tenant.ID, "Mesa 7", models.OrderStatusPreparando, false, 0)

	w := do105(t, r, http.MethodPatch, fmt.Sprintf("/orders/%s/status", ticket.ID), map[string]any{"status": "listo"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, fake.Calls, 1, "un push al pasar a listo")
	assert.Contains(t, fake.Calls[0].Payload.Title, "listo")
	assert.Contains(t, fake.Calls[0].Payload.Body, "Mesa 7")

	// El push NO se repite para otras transiciones.
	w = do105(t, r, http.MethodPatch, fmt.Sprintf("/orders/%s/status", ticket.ID), map[string]any{"status": "entregado"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Len(t, fake.Calls, 1, "entregado no dispara push")
}

// AC-F2: el sweep auto-entrega tickets PREPAGO huérfanos en 'listo' > 45 min
// (nadie los marcó entregado). Las cuentas de mesa sin pagar NUNCA se tocan
// (dinero pendiente) y los prepago recientes tampoco.
func TestSpec105F2_SweepAutoEntregaPrepagoHuerfano(t *testing.T) {
	db := setupOrdersF2DB(t)

	huerfano := seedTicketF2(t, db, "t1", "Pedido 8", models.OrderStatusListo, true, 50*time.Minute)
	prepagoReciente := seedTicketF2(t, db, "t1", "Pedido 9", models.OrderStatusListo, true, 10*time.Minute)
	mesaVieja := seedTicketF2(t, db, "t1", "Mesa 2", models.OrderStatusListo, false, 2*time.Hour)

	n, err := handlers.SweepOrphanOrders(db, 45*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "solo el prepago huérfano")

	var rowHuerfano, rowReciente, rowMesa models.OrderTicket
	require.NoError(t, db.First(&rowHuerfano, "id = ?", huerfano.ID).Error)
	assert.Equal(t, models.OrderStatusEntregado, rowHuerfano.Status)
	assert.NotNil(t, rowHuerfano.EntregadoAt)

	require.NoError(t, db.First(&rowReciente, "id = ?", prepagoReciente.ID).Error)
	assert.Equal(t, models.OrderStatusListo, rowReciente.Status)

	require.NoError(t, db.First(&rowMesa, "id = ?", mesaVieja.ID).Error)
	assert.Equal(t, models.OrderStatusListo, rowMesa.Status, "cuenta de mesa sin pagar es intocable")
}
