// Spec: specs/030-administracion-clientes-no-tienda/spec.md
package handlers_test

import (
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

// ── F030 — customer management handler tests (in-memory SQLite) ─────────────
//
// These tests exercise ListCustomers and GetCustomerHistory against a real
// schema (AutoMigrate of Customer + Sale + SaleItem). SQLite is enough: the
// aggregate query uses portable SUM/COUNT/MAX and a LOWER+LIKE search, no
// Postgres-only syntax. No Docker required.

func setupCustomerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Customer{},
		&models.Sale{},
		&models.SaleItem{},
	))
	return db
}

// customerRouter wires ListCustomers + GetCustomerHistory with the tenant id
// injected into the context, as the auth middleware would.
func customerRouter(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	inject := func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	}
	r.GET("/api/v1/customers", inject, handlers.ListCustomers(db))
	r.GET("/api/v1/customers/:id/history", inject, handlers.GetCustomerHistory(db))
	return r
}

// seedCustomer inserts a customer for the given tenant and returns its id.
func seedCustomer(t *testing.T, db *gorm.DB, tenantID, name, phone string) string {
	t.Helper()
	cust := models.Customer{TenantID: tenantID, Name: name, Phone: phone}
	require.NoError(t, db.Create(&cust).Error)
	return cust.ID
}

// seedSale inserts a sale linked to customerID (pass "" for an anonymous
// sale) with the given total and created_at.
func seedSale(t *testing.T, db *gorm.DB, tenantID, customerID string, total float64, when time.Time) {
	t.Helper()
	sale := models.Sale{
		TenantID:      tenantID,
		Total:         total,
		PaymentMethod: models.PaymentCash,
		PaymentStatus: "COMPLETED",
		PriceTier:     models.PriceTierRetail,
		Source:        models.SaleSourcePOS,
	}
	if customerID != "" {
		sale.CustomerID = &customerID
	}
	require.NoError(t, db.Create(&sale).Error)
	require.NoError(t,
		db.Model(&models.Sale{}).Where("id = ?", sale.ID).
			Update("created_at", when).Error)
}

func getCustomers(r http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	r.ServeHTTP(w, req)
	return w
}

// TestListCustomers_WithAggregates verifies GET /customers returns the
// tenant's customers each annotated with total_spent, purchase_count and
// last_purchase_at computed from sales (F030 AC-05 / T-06).
func TestListCustomers_WithAggregates(t *testing.T) {
	db := setupCustomerDB(t)
	const tenantID = "11111111-1111-1111-1111-111111111111"

	maria := seedCustomer(t, db, tenantID, "María Pérez", "3001112233")
	_ = seedCustomer(t, db, tenantID, "Carlos López", "3009998877") // no sales

	now := time.Now().UTC()
	seedSale(t, db, tenantID, maria, 12000, now.Add(-48*time.Hour))
	seedSale(t, db, tenantID, maria, 8000, now.Add(-2*time.Hour))
	// An anonymous sale must NOT inflate any customer's aggregates.
	seedSale(t, db, tenantID, "", 50000, now)

	w := getCustomers(customerRouter(db, tenantID), "/api/v1/customers")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data []struct {
			ID             string  `json:"id"`
			Name           string  `json:"name"`
			TotalSpent     float64 `json:"total_spent"`
			PurchaseCount  int64   `json:"purchase_count"`
			LastPurchaseAt *string `json:"last_purchase_at"`
		} `json:"data"`
		Meta struct {
			Total  int64 `json:"total"`
			Limit  int   `json:"limit"`
			Offset int   `json:"offset"`
		} `json:"meta"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 2, "deben venir los 2 clientes del tenant")
	assert.EqualValues(t, 2, resp.Meta.Total)

	byID := map[string]struct {
		total float64
		count int64
		last  *string
	}{}
	for _, c := range resp.Data {
		byID[c.ID] = struct {
			total float64
			count int64
			last  *string
		}{c.TotalSpent, c.PurchaseCount, c.LastPurchaseAt}
	}
	assert.EqualValues(t, 20000, byID[maria].total, "María: 12000 + 8000")
	assert.EqualValues(t, 2, byID[maria].count, "María: 2 compras")
	assert.NotNil(t, byID[maria].last, "María tiene last_purchase_at")

	// Carlos has no sales — aggregates must be zero and last_purchase_at nil.
	for _, c := range resp.Data {
		if c.Name == "Carlos López" {
			assert.EqualValues(t, 0, c.TotalSpent, "Carlos sin ventas: 0 gastado")
			assert.EqualValues(t, 0, c.PurchaseCount, "Carlos sin ventas: 0 compras")
			assert.Nil(t, c.LastPurchaseAt, "Carlos sin ventas: last_purchase_at nulo")
		}
	}
}

// TestListCustomers_SearchByNameAndPhone verifies the `q` query param does
// a case-insensitive substring match on name OR phone (F030 / T-06).
func TestListCustomers_SearchByNameAndPhone(t *testing.T) {
	db := setupCustomerDB(t)
	const tenantID = "22222222-2222-2222-2222-222222222222"

	seedCustomer(t, db, tenantID, "María Pérez", "3001112233")
	seedCustomer(t, db, tenantID, "Juan García", "3104445566")
	seedCustomer(t, db, tenantID, "Carlos López", "3009998877")

	r := customerRouter(db, tenantID)

	// Match by name fragment (case-insensitive).
	w := getCustomers(r, "/api/v1/customers?q=mar")
	require.Equal(t, http.StatusOK, w.Code)
	var byName struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &byName))
	require.Len(t, byName.Data, 1, "q=mar debe matchear solo a María")
	assert.Equal(t, "María Pérez", byName.Data[0].Name)

	// Match by phone fragment.
	w2 := getCustomers(r, "/api/v1/customers?q=310")
	require.Equal(t, http.StatusOK, w2.Code)
	var byPhone struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &byPhone))
	require.Len(t, byPhone.Data, 1, "q=310 debe matchear por teléfono")
	assert.Equal(t, "Juan García", byPhone.Data[0].Name)
}

// TestListCustomers_TenantScoped verifies a tenant only sees its own
// customers (Constitución Art. III).
func TestListCustomers_TenantScoped(t *testing.T) {
	db := setupCustomerDB(t)
	const tenantA = "33333333-3333-3333-3333-333333333333"
	const tenantB = "44444444-4444-4444-4444-444444444444"

	seedCustomer(t, db, tenantA, "Cliente A", "3000000001")
	seedCustomer(t, db, tenantB, "Cliente B", "3000000002")

	w := getCustomers(customerRouter(db, tenantA), "/api/v1/customers")
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "Cliente A", resp.Data[0].Name, "el tenant A no debe ver clientes del tenant B")
}

// TestListCustomers_Pagination verifies limit/offset paging (F030 §4).
func TestListCustomers_Pagination(t *testing.T) {
	db := setupCustomerDB(t)
	const tenantID = "55555555-5555-5555-5555-555555555555"

	for _, n := range []string{"Ana", "Beto", "Caro", "Dani", "Eva"} {
		seedCustomer(t, db, tenantID, n, "")
	}

	r := customerRouter(db, tenantID)
	w := getCustomers(r, "/api/v1/customers?limit=2&offset=0")
	require.Equal(t, http.StatusOK, w.Code)
	var page struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
		Meta struct {
			Total  int64 `json:"total"`
			Limit  int   `json:"limit"`
			Offset int   `json:"offset"`
		} `json:"meta"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.Len(t, page.Data, 2, "limit=2 debe devolver 2 filas")
	assert.EqualValues(t, 5, page.Meta.Total, "meta.total cuenta todo el conjunto")
	assert.Equal(t, "Ana", page.Data[0].Name, "orden alfabético")
	assert.Equal(t, "Beto", page.Data[1].Name)

	w2 := getCustomers(r, "/api/v1/customers?limit=2&offset=4")
	require.Equal(t, http.StatusOK, w2.Code)
	var page2 struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &page2))
	require.Len(t, page2.Data, 1, "offset=4 con 5 clientes deja 1 fila")
	assert.Equal(t, "Eva", page2.Data[0].Name)
}

// TestGetCustomerHistory_SummaryAndSales verifies the per-customer history
// returns customer + summary + sales list (F030 AC-06 / T-08).
func TestGetCustomerHistory_SummaryAndSales(t *testing.T) {
	db := setupCustomerDB(t)
	const tenantID = "66666666-6666-6666-6666-666666666666"

	maria := seedCustomer(t, db, tenantID, "María Pérez", "3001112233")
	now := time.Now().UTC()
	seedSale(t, db, tenantID, maria, 12000, now.Add(-72*time.Hour))
	seedSale(t, db, tenantID, maria, 8000, now.Add(-1*time.Hour))

	w := getCustomers(customerRouter(db, tenantID),
		"/api/v1/customers/"+maria+"/history")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Customer struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"customer"`
			Summary struct {
				TotalSpent      float64 `json:"total_spent"`
				PurchaseCount   int64   `json:"purchase_count"`
				LastPurchaseAt  *string `json:"last_purchase_at"`
				FirstPurchaseAt *string `json:"first_purchase_at"`
			} `json:"summary"`
			Sales []struct {
				ID    string  `json:"id"`
				Total float64 `json:"total"`
			} `json:"sales"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, maria, resp.Data.Customer.ID)
	assert.Equal(t, "María Pérez", resp.Data.Customer.Name)
	assert.EqualValues(t, 20000, resp.Data.Summary.TotalSpent)
	assert.EqualValues(t, 2, resp.Data.Summary.PurchaseCount)
	require.NotNil(t, resp.Data.Summary.LastPurchaseAt)
	require.NotNil(t, resp.Data.Summary.FirstPurchaseAt)
	require.Len(t, resp.Data.Sales, 2, "deben venir las 2 ventas")
	// Newest first.
	assert.EqualValues(t, 8000, resp.Data.Sales[0].Total, "venta más reciente primero")
}

// TestGetCustomerHistory_NoSales verifies a customer with zero sales gets a
// zeroed summary and an empty sales list (F030 — Carlos en T-91).
func TestGetCustomerHistory_NoSales(t *testing.T) {
	db := setupCustomerDB(t)
	const tenantID = "77777777-7777-7777-7777-777777777777"

	carlos := seedCustomer(t, db, tenantID, "Carlos López", "3009998877")

	w := getCustomers(customerRouter(db, tenantID),
		"/api/v1/customers/"+carlos+"/history")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Summary struct {
				TotalSpent     float64 `json:"total_spent"`
				PurchaseCount  int64   `json:"purchase_count"`
				LastPurchaseAt *string `json:"last_purchase_at"`
			} `json:"summary"`
			Sales []struct {
				ID string `json:"id"`
			} `json:"sales"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.EqualValues(t, 0, resp.Data.Summary.TotalSpent)
	assert.EqualValues(t, 0, resp.Data.Summary.PurchaseCount)
	assert.Nil(t, resp.Data.Summary.LastPurchaseAt)
	assert.Empty(t, resp.Data.Sales)
}

// TestGetCustomerHistory_CrossTenant404 verifies a customer id belonging to
// another tenant returns 404 — never leaks its existence (F030 AC-06 / T-08).
func TestGetCustomerHistory_CrossTenant404(t *testing.T) {
	db := setupCustomerDB(t)
	const tenantA = "88888888-8888-8888-8888-888888888888"
	const tenantB = "99999999-9999-9999-9999-999999999999"

	foreign := seedCustomer(t, db, tenantB, "Cliente Ajeno", "3000000099")

	w := getCustomers(customerRouter(db, tenantA),
		"/api/v1/customers/"+foreign+"/history")
	assert.Equal(t, http.StatusNotFound, w.Code,
		"un cliente de otro tenant debe devolver 404")
}
