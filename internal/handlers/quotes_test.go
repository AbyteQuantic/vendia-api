// Spec: specs/031-cotizaciones/spec.md
package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ── F031 — quotes handler suite (in-memory SQLite) ──────────────────────────
//
// SQLite is enough here: the CRUD path uses portable SQL, the folio
// sequence's SELECT FOR UPDATE degrades to a plain read on SQLite (which
// serialises writers anyway), and the CHECK constraint on status is
// portable. No Docker required.

func setupQuoteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Tenant{},
		&models.Customer{},
		&models.Product{},
		&models.Sale{},
		&models.SaleItem{},
		&models.Quote{},
		&models.QuoteItem{},
		&models.QuoteSequence{},
	))
	// Notifications uses a Postgres-specific `gen_random_uuid()` default
	// that SQLite can't parse. Rather than mutate the production model,
	// stand up an equivalent table by hand — the id is filled in by the
	// app's BeforeCreate hook in this test (same pattern as
	// table_sessions_test.go).
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS notifications (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			tenant_id TEXT NOT NULL,
			title TEXT NOT NULL,
			body TEXT DEFAULT '',
			type TEXT DEFAULT 'info',
			is_read INTEGER DEFAULT 0
		)
	`).Error)
	return db
}

// quoteRouter wires every quote endpoint with the tenant id + a fake
// user id injected into the context, as the auth middleware would.
func quoteRouter(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	inject := func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Set(middleware.UserIDKey, "99999999-9999-9999-9999-999999999999")
		c.Next()
	}
	g := r.Group("/api/v1", inject)
	g.GET("/quotes", handlers.ListQuotes(db))
	g.POST("/quotes", handlers.CreateQuote(db))
	g.GET("/quotes/:id", handlers.GetQuote(db))
	g.PATCH("/quotes/:id", handlers.UpdateQuote(db))
	g.DELETE("/quotes/:id", handlers.DeleteQuote(db))
	g.POST("/quotes/:id/send", handlers.SendQuote(db))
	g.POST("/quotes/:id/mark-status", handlers.MarkQuoteStatus(db))
	g.POST("/quotes/:id/convert", handlers.ConvertQuote(db))
	return r
}

func seedQuoteCustomer(t *testing.T, db *gorm.DB, tenantID, name string) string {
	t.Helper()
	cust := models.Customer{TenantID: tenantID, Name: name, Phone: "3001234567"}
	require.NoError(t, db.Create(&cust).Error)
	return cust.ID
}

func seedQuoteProduct(t *testing.T, db *gorm.DB, tenantID, name string, price float64, stock int) string {
	t.Helper()
	p := models.Product{TenantID: tenantID, Name: name, Price: price, Stock: stock}
	require.NoError(t, db.Create(&p).Error)
	return p.ID
}

func quoteReq(r http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	// Populate RemoteAddr so c.ClientIP() resolves a non-empty value —
	// http.NewRequest leaves it blank (unlike httptest.NewRequest).
	req.RemoteAddr = "203.0.113.7:54321"
	r.ServeHTTP(w, req)
	return w
}

// quoteResponse is the decoded shape of a single-quote response.
type quoteResponse struct {
	Data struct {
		ID            string  `json:"id"`
		Folio         string  `json:"folio"`
		Status        string  `json:"status"`
		Subtotal      float64 `json:"subtotal"`
		TaxAmount     float64 `json:"tax_amount"`
		Total         float64 `json:"total"`
		DiscountTotal float64 `json:"discount_total"`
		PublicToken   string  `json:"public_token"`
		SaleID        *string `json:"sale_id"`
		ReplacedByID  *string `json:"replaced_by_id"`
		Items         []struct {
			Name      string  `json:"name"`
			Quantity  float64 `json:"quantity"`
			UnitPrice float64 `json:"unit_price"`
			Subtotal  float64 `json:"subtotal"`
		} `json:"items"`
	} `json:"data"`
}

// TestCreateQuote_HappyPath verifies a quote can be created with a
// customer, two items, a global discount and a tax rate; that a folio is
// assigned and the totals are correct (Spec F031 T-11, AC-03, AC-04).
func TestCreateQuote_HappyPath(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantID = "11111111-1111-1111-1111-111111111111"
	customer := seedQuoteCustomer(t, db, tenantID, "Constructora ACME")
	product := seedQuoteProduct(t, db, tenantID, "Cemento 50kg", 28000, 100)

	w := quoteReq(quoteRouter(db, tenantID), http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id":    customer,
		"discount_total": 5000,
		"tax_rate":       0.19,
		"items": []map[string]any{
			{"product_id": product, "quantity": 2, "unit_price": 28000},
			{"name": "Mano de obra", "quantity": 1, "unit_price": 50000},
		},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp quoteResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, models.QuoteStatusDraft, resp.Data.Status, "arranca en borrador")
	assert.Regexp(t, `^COT-\d{4}-\d{4}$`, resp.Data.Folio, "folio COT-YYYY-NNNN")
	assert.NotEmpty(t, resp.Data.PublicToken, "se asigna un public_token")
	require.Len(t, resp.Data.Items, 2)

	// subtotal = 2*28000 + 1*50000 = 106000
	assert.EqualValues(t, 106000, resp.Data.Subtotal)
	// taxable = 106000 - 5000 = 101000; tax = 101000*0.19 = 19190
	assert.InDelta(t, 19190, resp.Data.TaxAmount, 0.01)
	// total = 101000 + 19190 = 120190
	assert.InDelta(t, 120190, resp.Data.Total, 0.01)
}

// TestCreateQuote_FoliosAreSequential verifies consecutive quotes for a
// tenant get consecutive folios (Spec F031 AC-04).
func TestCreateQuote_FoliosAreSequential(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantID = "22222222-2222-2222-2222-222222222222"
	customer := seedQuoteCustomer(t, db, tenantID, "Cliente")
	r := quoteRouter(db, tenantID)

	body := map[string]any{
		"customer_id": customer,
		"items":       []map[string]any{{"name": "Servicio", "quantity": 1, "unit_price": 1000}},
	}

	var folios []string
	for i := 0; i < 3; i++ {
		w := quoteReq(r, http.MethodPost, "/api/v1/quotes", body)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		var resp quoteResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		folios = append(folios, resp.Data.Folio)
	}
	year := time.Now().Year()
	assert.Equal(t, []string{
		"COT-" + itoa(year) + "-0001",
		"COT-" + itoa(year) + "-0002",
		"COT-" + itoa(year) + "-0003",
	}, folios)
}

// TestCreateQuote_RejectsForeignCustomer verifies a customer_id from
// another tenant returns 400 (Spec F031 T-13, Constitución Art. III).
func TestCreateQuote_RejectsForeignCustomer(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantA = "33333333-3333-3333-3333-333333333333"
	const tenantB = "44444444-4444-4444-4444-444444444444"
	foreign := seedQuoteCustomer(t, db, tenantB, "Cliente Ajeno")

	w := quoteReq(quoteRouter(db, tenantA), http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id": foreign,
		"items":       []map[string]any{{"name": "X", "quantity": 1, "unit_price": 100}},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "cliente de otro tenant → 400")
}

// TestCreateQuote_RejectsForeignProduct verifies an item product_id from
// another tenant returns 400 (Spec F031 T-13).
func TestCreateQuote_RejectsForeignProduct(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantA = "55555555-5555-5555-5555-555555555555"
	const tenantB = "66666666-6666-6666-6666-666666666666"
	customer := seedQuoteCustomer(t, db, tenantA, "Cliente")
	foreignProduct := seedQuoteProduct(t, db, tenantB, "Producto Ajeno", 1000, 10)

	w := quoteReq(quoteRouter(db, tenantA), http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id": customer,
		"items":       []map[string]any{{"product_id": foreignProduct, "quantity": 1, "unit_price": 1000}},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "producto de otro tenant → 400")
}

// TestCreateQuote_RequiresCustomer verifies a quote without a customer
// is rejected (Spec §4 — cliente obligatorio).
func TestCreateQuote_RequiresCustomer(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantID = "77777777-7777-7777-7777-777777777777"

	w := quoteReq(quoteRouter(db, tenantID), http.MethodPost, "/api/v1/quotes", map[string]any{
		"items": []map[string]any{{"name": "X", "quantity": 1, "unit_price": 100}},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "sin cliente → 400")
}

// TestCreateQuote_RequiresItems verifies a quote with no items is rejected.
func TestCreateQuote_RequiresItems(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantID = "88888888-8888-8888-8888-888888888888"
	customer := seedQuoteCustomer(t, db, tenantID, "Cliente")

	w := quoteReq(quoteRouter(db, tenantID), http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id": customer,
		"items":       []map[string]any{},
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, "sin ítems → 400")
}

// TestUpdateQuote_DraftOverwritten verifies editing a `borrador` quote
// overwrites it in place — same id, new totals (Spec F031 AC-11).
func TestUpdateQuote_DraftOverwritten(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantID = "12121212-1212-1212-1212-121212121212"
	customer := seedQuoteCustomer(t, db, tenantID, "Cliente")
	r := quoteRouter(db, tenantID)

	created := quoteReq(r, http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id": customer,
		"items":       []map[string]any{{"name": "Item viejo", "quantity": 1, "unit_price": 1000}},
	})
	require.Equal(t, http.StatusCreated, created.Code)
	var c quoteResponse
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &c))

	upd := quoteReq(r, http.MethodPatch, "/api/v1/quotes/"+c.Data.ID, map[string]any{
		"customer_id": customer,
		"items":       []map[string]any{{"name": "Item nuevo", "quantity": 3, "unit_price": 2000}},
	})
	require.Equal(t, http.StatusOK, upd.Code, upd.Body.String())
	var u quoteResponse
	require.NoError(t, json.Unmarshal(upd.Body.Bytes(), &u))

	assert.Equal(t, c.Data.ID, u.Data.ID, "el borrador se sobreescribe — mismo id")
	assert.Equal(t, models.QuoteStatusDraft, u.Data.Status)
	require.Len(t, u.Data.Items, 1)
	assert.Equal(t, "Item nuevo", u.Data.Items[0].Name)
	assert.EqualValues(t, 6000, u.Data.Subtotal, "3 * 2000")
}

// TestUpdateQuote_SentCreatesV2 verifies editing a `enviada` quote
// creates a v2 (folio -V2) and marks the v1 `reemplazada` (AC-11).
func TestUpdateQuote_SentCreatesV2(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantID = "13131313-1313-1313-1313-131313131313"
	customer := seedQuoteCustomer(t, db, tenantID, "Cliente")
	r := quoteRouter(db, tenantID)

	created := quoteReq(r, http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id": customer,
		"items":       []map[string]any{{"name": "Item", "quantity": 1, "unit_price": 1000}},
	})
	var c quoteResponse
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &c))

	// Send it so it leaves borrador.
	require.Equal(t, http.StatusOK,
		quoteReq(r, http.MethodPost, "/api/v1/quotes/"+c.Data.ID+"/send", nil).Code)

	upd := quoteReq(r, http.MethodPatch, "/api/v1/quotes/"+c.Data.ID, map[string]any{
		"customer_id": customer,
		"items":       []map[string]any{{"name": "Item v2", "quantity": 2, "unit_price": 1500}},
	})
	require.Equal(t, http.StatusOK, upd.Code, upd.Body.String())
	var v2 quoteResponse
	require.NoError(t, json.Unmarshal(upd.Body.Bytes(), &v2))

	assert.NotEqual(t, c.Data.ID, v2.Data.ID, "v2 es una cotización nueva")
	assert.Contains(t, v2.Data.Folio, "-V2", "el folio v2 lleva sufijo -V2")
	assert.Equal(t, models.QuoteStatusDraft, v2.Data.Status, "v2 arranca en borrador")

	// v1 must now be `reemplazada` pointing at v2.
	var v1 models.Quote
	require.NoError(t, db.Where("id = ?", c.Data.ID).First(&v1).Error)
	assert.Equal(t, models.QuoteStatusReplaced, v1.Status)
	require.NotNil(t, v1.ReplacedByID)
	assert.Equal(t, v2.Data.ID, *v1.ReplacedByID)
}

// TestUpdateQuote_ApprovedRejected verifies editing an `aprobada` quote
// is rejected with 400 (Spec F031 T-13).
func TestUpdateQuote_ApprovedRejected(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantID = "14141414-1414-1414-1414-141414141414"
	customer := seedQuoteCustomer(t, db, tenantID, "Cliente")
	r := quoteRouter(db, tenantID)

	created := quoteReq(r, http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id": customer,
		"items":       []map[string]any{{"name": "Item", "quantity": 1, "unit_price": 1000}},
	})
	var c quoteResponse
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &c))

	require.Equal(t, http.StatusOK,
		quoteReq(r, http.MethodPost, "/api/v1/quotes/"+c.Data.ID+"/send", nil).Code)
	require.Equal(t, http.StatusOK,
		quoteReq(r, http.MethodPost, "/api/v1/quotes/"+c.Data.ID+"/mark-status",
			map[string]any{"status": models.QuoteStatusApproved}).Code)

	upd := quoteReq(r, http.MethodPatch, "/api/v1/quotes/"+c.Data.ID, map[string]any{
		"customer_id": customer,
		"items":       []map[string]any{{"name": "X", "quantity": 1, "unit_price": 1}},
	})
	assert.Equal(t, http.StatusBadRequest, upd.Code,
		"editar una cotización aprobada → 400")
}

// TestSendQuote_DraftToSent verifies Send moves borrador → enviada and
// stamps sent_at (Spec F031 T-14).
func TestSendQuote_DraftToSent(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantID = "15151515-1515-1515-1515-151515151515"
	customer := seedQuoteCustomer(t, db, tenantID, "Cliente")
	r := quoteRouter(db, tenantID)

	created := quoteReq(r, http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id": customer,
		"items":       []map[string]any{{"name": "Item", "quantity": 1, "unit_price": 1000}},
	})
	var c quoteResponse
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &c))

	w := quoteReq(r, http.MethodPost, "/api/v1/quotes/"+c.Data.ID+"/send", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var sent quoteResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &sent))
	assert.Equal(t, models.QuoteStatusSent, sent.Data.Status)

	var stored models.Quote
	require.NoError(t, db.Where("id = ?", c.Data.ID).First(&stored).Error)
	require.NotNil(t, stored.SentAt, "sent_at debe quedar marcado")
}

// TestSendQuote_RejectsNonDraft verifies Send fails on an already-sent
// quote (Spec F031 T-14 — rechaza si estado ≠ borrador).
func TestSendQuote_RejectsNonDraft(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantID = "16161616-1616-1616-1616-161616161616"
	customer := seedQuoteCustomer(t, db, tenantID, "Cliente")
	r := quoteRouter(db, tenantID)

	created := quoteReq(r, http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id": customer,
		"items":       []map[string]any{{"name": "Item", "quantity": 1, "unit_price": 1000}},
	})
	var c quoteResponse
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &c))

	require.Equal(t, http.StatusOK,
		quoteReq(r, http.MethodPost, "/api/v1/quotes/"+c.Data.ID+"/send", nil).Code)
	// Second send must fail.
	w := quoteReq(r, http.MethodPost, "/api/v1/quotes/"+c.Data.ID+"/send", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code, "reenviar vía /send → 400")
}

// TestDeleteQuote_OnlyDraft verifies a draft can be deleted but a sent
// quote cannot (Spec plan §4 — auditoría).
func TestDeleteQuote_OnlyDraft(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantID = "17171717-1717-1717-1717-171717171717"
	customer := seedQuoteCustomer(t, db, tenantID, "Cliente")
	r := quoteRouter(db, tenantID)

	mk := func() string {
		w := quoteReq(r, http.MethodPost, "/api/v1/quotes", map[string]any{
			"customer_id": customer,
			"items":       []map[string]any{{"name": "Item", "quantity": 1, "unit_price": 1000}},
		})
		var c quoteResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &c))
		return c.Data.ID
	}

	draftID := mk()
	assert.Equal(t, http.StatusOK,
		quoteReq(r, http.MethodDelete, "/api/v1/quotes/"+draftID, nil).Code,
		"un borrador se puede eliminar")

	sentID := mk()
	require.Equal(t, http.StatusOK,
		quoteReq(r, http.MethodPost, "/api/v1/quotes/"+sentID+"/send", nil).Code)
	assert.Equal(t, http.StatusBadRequest,
		quoteReq(r, http.MethodDelete, "/api/v1/quotes/"+sentID, nil).Code,
		"una cotización enviada NO se puede eliminar")
}

// TestConvertQuote_FromApproved verifies converting an approved quote
// creates a sale linked via sale_id, with inventory discounted
// (Spec F031 AC-09, T-15).
func TestConvertQuote_FromApproved(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantID = "18181818-1818-1818-1818-181818181818"
	customer := seedQuoteCustomer(t, db, tenantID, "Constructora ACME")
	product := seedQuoteProduct(t, db, tenantID, "Cemento", 28000, 100)
	r := quoteRouter(db, tenantID)

	created := quoteReq(r, http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id": customer,
		"items": []map[string]any{
			{"product_id": product, "quantity": 4, "unit_price": 28000},
			{"name": "Instalación", "quantity": 1, "unit_price": 30000},
		},
	})
	var c quoteResponse
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &c))

	// Drive it to aprobada.
	require.Equal(t, http.StatusOK,
		quoteReq(r, http.MethodPost, "/api/v1/quotes/"+c.Data.ID+"/send", nil).Code)
	require.Equal(t, http.StatusOK,
		quoteReq(r, http.MethodPost, "/api/v1/quotes/"+c.Data.ID+"/mark-status",
			map[string]any{"status": models.QuoteStatusApproved}).Code)

	conv := quoteReq(r, http.MethodPost, "/api/v1/quotes/"+c.Data.ID+"/convert", nil)
	require.Equal(t, http.StatusCreated, conv.Code, conv.Body.String())

	var convResp struct {
		Data struct {
			SaleID string `json:"sale_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(conv.Body.Bytes(), &convResp))
	require.NotEmpty(t, convResp.Data.SaleID)

	// Quote is now convertida and linked.
	var stored models.Quote
	require.NoError(t, db.Where("id = ?", c.Data.ID).First(&stored).Error)
	assert.Equal(t, models.QuoteStatusConverted, stored.Status)
	require.NotNil(t, stored.SaleID)
	assert.Equal(t, convResp.Data.SaleID, *stored.SaleID)

	// Sale exists, linked back to the quote, with the right total.
	var sale models.Sale
	require.NoError(t, db.Preload("Items").
		Where("id = ?", convResp.Data.SaleID).First(&sale).Error)
	require.NotNil(t, sale.QuoteID)
	assert.Equal(t, c.Data.ID, *sale.QuoteID)
	assert.EqualValues(t, 142000, sale.Total, "4*28000 + 30000")
	assert.Len(t, sale.Items, 2)

	// Inventory was discounted: 100 - 4 = 96.
	var p models.Product
	require.NoError(t, db.Where("id = ?", product).First(&p).Error)
	assert.Equal(t, 96, p.Stock, "el inventario se descuenta al convertir")
}

// TestConvertQuote_RejectsNonApproved verifies convert fails when the
// quote is not in `aprobada` (Spec F031 AC-09 — solo desde aprobada).
func TestConvertQuote_RejectsNonApproved(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantID = "19191919-1919-1919-1919-191919191919"
	customer := seedQuoteCustomer(t, db, tenantID, "Cliente")
	r := quoteRouter(db, tenantID)

	created := quoteReq(r, http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id": customer,
		"items":       []map[string]any{{"name": "Item", "quantity": 1, "unit_price": 1000}},
	})
	var c quoteResponse
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &c))

	// Still a draft — convert must fail.
	w := quoteReq(r, http.MethodPost, "/api/v1/quotes/"+c.Data.ID+"/convert", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"convertir una cotización en borrador → 400")
}

// TestListQuotes_FilterByStatus verifies the status filter (Spec F031 AC-12).
func TestListQuotes_FilterByStatus(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantID = "21212121-2121-2121-2121-212121212121"
	customer := seedQuoteCustomer(t, db, tenantID, "Cliente")
	r := quoteRouter(db, tenantID)

	mkBody := map[string]any{
		"customer_id": customer,
		"items":       []map[string]any{{"name": "Item", "quantity": 1, "unit_price": 1000}},
	}
	// One draft, one sent.
	quoteReq(r, http.MethodPost, "/api/v1/quotes", mkBody)
	sent := quoteReq(r, http.MethodPost, "/api/v1/quotes", mkBody)
	var s quoteResponse
	require.NoError(t, json.Unmarshal(sent.Body.Bytes(), &s))
	require.Equal(t, http.StatusOK,
		quoteReq(r, http.MethodPost, "/api/v1/quotes/"+s.Data.ID+"/send", nil).Code)

	w := quoteReq(r, http.MethodGet, "/api/v1/quotes?status=enviada", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var list struct {
		Data  []struct{ Status string `json:"status"` } `json:"data"`
		Total int64                                       `json:"total"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	assert.EqualValues(t, 1, list.Total)
	require.Len(t, list.Data, 1)
	assert.Equal(t, models.QuoteStatusSent, list.Data[0].Status)
}

// TestListQuotes_TenantScoped verifies a tenant never sees another
// tenant's quotes (Constitución Art. III).
func TestListQuotes_TenantScoped(t *testing.T) {
	db := setupQuoteDB(t)
	const tenantA = "31313131-3131-3131-3131-313131313131"
	const tenantB = "32323232-3232-3232-3232-323232323232"
	custA := seedQuoteCustomer(t, db, tenantA, "Cliente A")
	custB := seedQuoteCustomer(t, db, tenantB, "Cliente B")

	quoteReq(quoteRouter(db, tenantA), http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id": custA,
		"items":       []map[string]any{{"name": "A", "quantity": 1, "unit_price": 100}},
	})
	quoteReq(quoteRouter(db, tenantB), http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id": custB,
		"items":       []map[string]any{{"name": "B", "quantity": 1, "unit_price": 100}},
	})

	w := quoteReq(quoteRouter(db, tenantA), http.MethodGet, "/api/v1/quotes", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var list struct {
		Total int64 `json:"total"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	assert.EqualValues(t, 1, list.Total, "el tenant A solo ve su cotización")
}

// itoa is a tiny int→string helper to keep the test free of strconv noise.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
