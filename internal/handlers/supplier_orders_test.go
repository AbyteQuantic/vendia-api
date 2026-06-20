// Spec: specs/075-proveedores-b2b/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupOrderDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Tenant{}, &models.Product{}, &models.SupplierOrder{}))
	return db
}

func mkT(t *testing.T, db *gorm.DB, id, name, phone string, types []string) {
	t.Helper()
	require.NoError(t, db.Create(&models.Tenant{
		BaseModel: models.BaseModel{ID: id}, OwnerName: "o", Phone: phone, PasswordHash: "x",
		BusinessName: name, BusinessTypes: types, SaleTypes: []string{"contado"},
	}).Error)
}

func mountOrders(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, tenantID); c.Next() })
	r.GET("/suppliers/:uuid/catalog", handlers.SupplierCatalog(db))
	r.POST("/suppliers/:uuid/orders", handlers.PlaceSupplierOrder(db))
	r.GET("/supplier/inbox", handlers.SupplierInbox(db))
	r.PATCH("/supplier/orders/:orderId", handlers.UpdateSupplierOrderStatus(db))
	return r
}

func TestSupplierCatalogAndOrderFlow(t *testing.T) {
	db := setupOrderDB(t)
	mkT(t, db, "prov1", "El Tomate", "3001112222", []string{"proveedor_agricola"})
	mkT(t, db, "store1", "Tienda Rosa", "3009998888", []string{"tienda_barrio"})
	require.NoError(t, db.Create(&models.Product{BaseModel: models.BaseModel{ID: "p1"}, TenantID: "prov1", Name: "Tomate", Price: 45000, Stock: 30}).Error)

	// La tienda ve el catálogo del proveedor.
	rStore := mountOrders(db, "store1")
	w := doJSON(t, rStore, http.MethodGet, "/suppliers/prov1/catalog", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Tomate")

	// Pedir a un NO-proveedor → 404.
	w404 := doJSON(t, rStore, http.MethodGet, "/suppliers/store1/catalog", nil)
	assert.Equal(t, http.StatusNotFound, w404.Code)

	// La tienda hace un pedido.
	wOrder := doJSON(t, rStore, http.MethodPost, "/suppliers/prov1/orders", map[string]any{
		"items":           []map[string]any{{"product_id": "p1", "name": "Tomate", "quantity": 2, "price": 45000}},
		"delivery_choice": "tienda_recoge",
	})
	require.Equal(t, http.StatusCreated, wOrder.Code)
	var resp struct {
		Data struct {
			Order       models.SupplierOrder `json:"order"`
			WhatsAppURL string               `json:"whatsapp_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(wOrder.Body.Bytes(), &resp))
	assert.Equal(t, float64(90000), resp.Data.Order.TotalAmount)
	assert.Equal(t, "tienda_recoge", resp.Data.Order.DeliveryChoice)
	assert.Contains(t, resp.Data.WhatsAppURL, "wa.me")

	// Pedido sin items → 400.
	wBad := doJSON(t, rStore, http.MethodPost, "/suppliers/prov1/orders", map[string]any{"items": []any{}})
	assert.Equal(t, http.StatusBadRequest, wBad.Code)

	// El proveedor ve su buzón (1 pedido) y lo confirma.
	rProv := mountOrders(db, "prov1")
	wInbox := doJSON(t, rProv, http.MethodGet, "/supplier/inbox", nil)
	require.Equal(t, http.StatusOK, wInbox.Code)
	var inbox struct {
		Data []models.SupplierOrder `json:"data"`
	}
	require.NoError(t, json.Unmarshal(wInbox.Body.Bytes(), &inbox))
	require.Len(t, inbox.Data, 1)
	oid := inbox.Data[0].ID

	wUpd := doJSON(t, rProv, http.MethodPatch, "/supplier/orders/"+oid, map[string]any{"status": "confirmado"})
	assert.Equal(t, http.StatusOK, wUpd.Code)

	// Otro tenant NO puede tocar ese pedido.
	rOther := mountOrders(db, "store1")
	wForbidden := doJSON(t, rOther, http.MethodPatch, "/supplier/orders/"+oid, map[string]any{"status": "cancelado"})
	assert.Equal(t, http.StatusNotFound, wForbidden.Code)
}
