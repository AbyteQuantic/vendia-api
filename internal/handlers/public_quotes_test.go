// Spec: specs/031-cotizaciones/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// publicQuoteRouter wires the two public quote endpoints — no auth
// middleware, the token is the only credential.
func publicQuoteRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/public/quotes/:token", handlers.GetPublicQuote(db))
	r.POST("/api/v1/public/quotes/:token/decide", handlers.DecidePublicQuote(db))
	return r
}

// seedPublicQuote inserts a tenant + customer + quote in the given state
// and returns the public token.
func seedPublicQuote(t *testing.T, db *gorm.DB, status string, validUntil time.Time) (token, quoteID string) {
	t.Helper()
	const tenantID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	tenant := models.Tenant{
		BaseModel:    models.BaseModel{ID: tenantID},
		OwnerName:    "Dueño",
		Phone:        "300" + padTok(),
		PasswordHash: "x",
		BusinessName: "Ferretería Demo",
		SaleTypes:    []string{"contado"},
	}
	db.Create(&tenant) // best-effort — phone uniqueness not critical here

	cust := models.Customer{TenantID: tenantID, Name: "María Pérez", Phone: "3001112233"}
	require.NoError(t, db.Create(&cust).Error)

	tok := "bbbbbbbb-bbbb-bbbb-bbbb-" + padTok()
	quote := models.Quote{
		TenantID:    tenantID,
		CustomerID:  cust.ID,
		Folio:       "COT-2026-0001",
		Status:      status,
		ValidUntil:  validUntil,
		Subtotal:    10000,
		Total:       10000,
		PublicToken: tok,
		Items: []models.QuoteItem{
			{Name: "Cemento", Quantity: 1, UnitPrice: 10000, Subtotal: 10000},
		},
	}
	require.NoError(t, db.Create(&quote).Error)
	return tok, quote.ID
}

var tokCounter int

func padTok() string {
	tokCounter++
	s := []byte("000000000000")
	n := tokCounter
	i := len(s) - 1
	for n > 0 && i >= 0 {
		s[i] = byte('0' + n%10)
		n /= 10
		i--
	}
	return string(s)
}

// TestGetPublicQuote_ValidToken verifies a sent quote renders with
// branding + items and exposes can_decide=true (Spec F031 T-16, AC-07).
func TestGetPublicQuote_ValidToken(t *testing.T) {
	db := setupQuoteDB(t)
	token, _ := seedPublicQuote(t, db, models.QuoteStatusSent, time.Now().Add(72*time.Hour))

	w := quoteReq(publicQuoteRouter(db), http.MethodGet, "/api/v1/public/quotes/"+token, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Folio     string `json:"folio"`
			Status    string `json:"status"`
			CanDecide bool   `json:"can_decide"`
			Items     []any  `json:"items"`
			Business  struct {
				Name string `json:"name"`
			} `json:"business"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "COT-2026-0001", resp.Data.Folio)
	assert.Equal(t, models.QuoteStatusSent, resp.Data.Status)
	assert.True(t, resp.Data.CanDecide, "una cotización enviada permite decidir")
	assert.Equal(t, "Ferretería Demo", resp.Data.Business.Name, "branding del negocio")
	assert.Len(t, resp.Data.Items, 1)
}

// TestGetPublicQuote_InvalidToken verifies an unknown / malformed token
// returns 404 (Spec F031 T-16).
func TestGetPublicQuote_InvalidToken(t *testing.T) {
	db := setupQuoteDB(t)
	r := publicQuoteRouter(db)

	// Malformed (not a UUID).
	assert.Equal(t, http.StatusNotFound,
		quoteReq(r, http.MethodGet, "/api/v1/public/quotes/not-a-uuid", nil).Code)

	// Well-formed but unknown.
	assert.Equal(t, http.StatusNotFound,
		quoteReq(r, http.MethodGet,
			"/api/v1/public/quotes/cccccccc-cccc-cccc-cccc-cccccccccccc", nil).Code)
}

// TestGetPublicQuote_LazyExpire verifies a `enviada` quote past its
// valid_until is lazily flipped to `vencida` on read, with the decide
// buttons hidden (Spec F031 T-16, plan D7).
func TestGetPublicQuote_LazyExpire(t *testing.T) {
	db := setupQuoteDB(t)
	token, quoteID := seedPublicQuote(t, db,
		models.QuoteStatusSent, time.Now().Add(-1*time.Hour))

	w := quoteReq(publicQuoteRouter(db), http.MethodGet, "/api/v1/public/quotes/"+token, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Status    string `json:"status"`
			CanDecide bool   `json:"can_decide"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.QuoteStatusExpired, resp.Data.Status,
		"una cotización vencida se marca vencida al leer (lazy expire)")
	assert.False(t, resp.Data.CanDecide, "una vencida no muestra botones")

	// The DB row was actually updated.
	var stored models.Quote
	require.NoError(t, db.Where("id = ?", quoteID).First(&stored).Error)
	assert.Equal(t, models.QuoteStatusExpired, stored.Status)
}

// TestDecidePublicQuote_Approve verifies a sent quote can be approved
// from the public link, recording IP + timestamp (Spec F031 T-18, AC-08).
func TestDecidePublicQuote_Approve(t *testing.T) {
	db := setupQuoteDB(t)
	token, quoteID := seedPublicQuote(t, db,
		models.QuoteStatusSent, time.Now().Add(72*time.Hour))

	w := quoteReq(publicQuoteRouter(db), http.MethodPost,
		"/api/v1/public/quotes/"+token+"/decide",
		map[string]any{"decision": "approve"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var stored models.Quote
	require.NoError(t, db.Where("id = ?", quoteID).First(&stored).Error)
	assert.Equal(t, models.QuoteStatusApproved, stored.Status)
	require.NotNil(t, stored.DecidedAt, "decided_at debe quedar marcado")
	assert.NotEmpty(t, stored.DecidedByIP, "la IP del cliente queda como evidencia")

	// A notification for the owner was created (AC-08).
	var notifCount int64
	db.Model(&models.Notification{}).
		Where("tenant_id = ? AND type = ?", stored.TenantID, "quote_decision").
		Count(&notifCount)
	assert.EqualValues(t, 1, notifCount, "el dueño recibe una notificación")
}

// TestDecidePublicQuote_Reject verifies the reject path.
func TestDecidePublicQuote_Reject(t *testing.T) {
	db := setupQuoteDB(t)
	token, quoteID := seedPublicQuote(t, db,
		models.QuoteStatusSent, time.Now().Add(72*time.Hour))

	w := quoteReq(publicQuoteRouter(db), http.MethodPost,
		"/api/v1/public/quotes/"+token+"/decide",
		map[string]any{"decision": "reject"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var stored models.Quote
	require.NoError(t, db.Where("id = ?", quoteID).First(&stored).Error)
	assert.Equal(t, models.QuoteStatusRejected, stored.Status)
}

// TestDecidePublicQuote_AlreadyDecided verifies a second decision on an
// already-approved quote is rejected with 400 (Spec F031 T-18).
func TestDecidePublicQuote_AlreadyDecided(t *testing.T) {
	db := setupQuoteDB(t)
	token, _ := seedPublicQuote(t, db,
		models.QuoteStatusApproved, time.Now().Add(72*time.Hour))

	w := quoteReq(publicQuoteRouter(db), http.MethodPost,
		"/api/v1/public/quotes/"+token+"/decide",
		map[string]any{"decision": "reject"})
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"una cotización ya decidida no acepta otra respuesta")
}

// TestDecidePublicQuote_ExpiredRejected verifies a quote past its
// validity cannot be decided — the lazy expire turns it into `vencida`
// which fails the FSM check (Spec F031 T-18, plan R2).
func TestDecidePublicQuote_ExpiredRejected(t *testing.T) {
	db := setupQuoteDB(t)
	token, _ := seedPublicQuote(t, db,
		models.QuoteStatusSent, time.Now().Add(-1*time.Hour))

	w := quoteReq(publicQuoteRouter(db), http.MethodPost,
		"/api/v1/public/quotes/"+token+"/decide",
		map[string]any{"decision": "approve"})
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"una cotización vencida no se puede aprobar")
}

// TestDecidePublicQuote_InvalidDecision verifies a bad decision value
// returns 400.
func TestDecidePublicQuote_InvalidDecision(t *testing.T) {
	db := setupQuoteDB(t)
	token, _ := seedPublicQuote(t, db,
		models.QuoteStatusSent, time.Now().Add(72*time.Hour))

	w := quoteReq(publicQuoteRouter(db), http.MethodPost,
		"/api/v1/public/quotes/"+token+"/decide",
		map[string]any{"decision": "maybe"})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
