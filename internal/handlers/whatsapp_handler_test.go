package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupWhatsAppTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Tenant{},
		&models.Customer{},
		&models.Sale{},
		&models.SaleItem{},
	))
	db.Create(&models.Tenant{
		BaseModel:    models.BaseModel{ID: "t-1"},
		BusinessName: "Tienda Test",
	})
	return db
}

func whatsappRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/sales/:uuid/send-receipt", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, "t-1")
		SendReceipt(db)(c)
	})
	return r
}

func sendReceiptResp(t *testing.T, r *gin.Engine, saleID string) (int, map[string]any) {
	req, _ := http.NewRequest(http.MethodPost, "/sales/"+saleID+"/send-receipt", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var body struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body.Data
}

// Venta de MOSTRADOR (anónima, sin cliente — válida por F030): el recibo
// debe poder enviarse igual. Sin teléfono, la URL es `wa.me/?text=…`,
// que abre WhatsApp con el mensaje listo y deja elegir el contacto.
func TestSendReceipt_AnonymousSaleUsesContactPicker(t *testing.T) {
	db := setupWhatsAppTestDB(t)
	db.Create(&models.Sale{
		BaseModel: models.BaseModel{ID: "s-anon"},
		TenantID:  "t-1",
		Total:     4600,
	})

	code, data := sendReceiptResp(t, whatsappRouter(db), "s-anon")
	require.Equal(t, http.StatusOK, code)

	waURL, _ := data["whatsapp_url"].(string)
	assert.True(t, strings.HasPrefix(waURL, "https://wa.me/?text="),
		"sin cliente la URL debe ser el selector de contactos, got %q", waURL)
	msg, _ := data["message"].(string)
	assert.Contains(t, msg, "Tienda Test")
	assert.Contains(t, msg, "4.600")
	// El link viejo a vendia.co estaba MUERTO (dominio equivocado, ruta
	// inexistente) — el mensaje no debe incluirlo.
	assert.NotContains(t, msg, "vendia.co")
}

// Venta con cliente: la URL apunta al teléfono del cliente (formato 57…).
func TestSendReceipt_WithCustomerTargetsPhone(t *testing.T) {
	db := setupWhatsAppTestDB(t)
	db.Create(&models.Customer{
		BaseModel: models.BaseModel{ID: "c-1"},
		TenantID:  "t-1",
		Name:      "Ana",
		Phone:     "3001234567",
	})
	cid := "c-1"
	db.Create(&models.Sale{
		BaseModel:  models.BaseModel{ID: "s-cli"},
		TenantID:   "t-1",
		Total:      12000,
		CustomerID: &cid,
	})

	code, data := sendReceiptResp(t, whatsappRouter(db), "s-cli")
	require.Equal(t, http.StatusOK, code)

	waURL, _ := data["whatsapp_url"].(string)
	assert.True(t, strings.HasPrefix(waURL, "https://wa.me/573001234567?text="),
		"con cliente la URL debe ir a su teléfono, got %q", waURL)
}
