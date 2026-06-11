// Spec: specs/042-modulo-eventos/spec.md
package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"
)

func setupPublicEventsDB(t *testing.T) (*gorm.DB, *models.Tenant) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Tenant{}, &models.Event{}, &models.EventRegistration{},
		&models.EventScan{}, &models.EventPayment{}, &models.Customer{},
		&models.Sale{}, &models.SaleItem{},
	))
	slug := "mi-tienda"
	tenant := models.Tenant{OwnerName: "Org", Phone: "3000000000", StoreSlug: &slug}
	require.NoError(t, db.Create(&tenant).Error)
	return db, &tenant
}

func publicEventsRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/store/:slug/events", PublicListEvents(db))
	r.GET("/api/v1/store/:slug/events/:id", PublicGetEvent(db))
	r.POST("/api/v1/store/:slug/events/:id/register", PublicRegisterEvent(db))
	r.GET("/api/v1/store/:slug/carnet/:token", PublicGetCarnet(db))
	r.POST("/api/v1/store/:slug/my-event-registration", PublicFindRegistration(db))
	return r
}

// seedPublished inserts a published event for the tenant via the service.
func seedPublished(t *testing.T, db *gorm.DB, tenantID string, price, capacity int) *models.Event {
	t.Helper()
	svc := services.NewEventService(db)
	ev, err := svc.Create(tenantID, &models.Event{
		Type: models.EventTypeCurso, Title: "Curso", Modality: models.EventModalityVirtual,
		Capacity: capacity, Price: int64(price),
	})
	require.NoError(t, err)
	_, err = svc.Publish(tenantID, ev.ID)
	require.NoError(t, err)
	return ev
}

func TestPublicListEvents_OnlyPublished(t *testing.T) {
	db, tenant := setupPublicEventsDB(t)
	seedPublished(t, db, tenant.ID, 100000, 10)
	// a draft event must NOT surface publicly
	_, err := services.NewEventService(db).Create(tenant.ID, &models.Event{
		Type: models.EventTypeOtro, Title: "Borrador", Modality: models.EventModalityPresencial,
	})
	require.NoError(t, err)

	r := publicEventsRouter(db)
	w := reqJSON(r, http.MethodGet, "/api/v1/store/mi-tienda/events", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, models.EventStatusPublicado, resp.Data[0].Status)
}

func TestPublicListEvents_HidesFinished(t *testing.T) {
	db, tenant := setupPublicEventsDB(t)
	// Próximo (fin en el futuro) → visible.
	upcoming := seedPublished(t, db, tenant.ID, 100000, 10)
	require.NoError(t, db.Model(&models.Event{}).Where("id = ?", upcoming.ID).
		Update("end_at", time.Now().Add(48*time.Hour)).Error)
	// Finalizado (fin en el pasado) → oculto del catálogo.
	finished := seedPublished(t, db, tenant.ID, 100000, 10)
	require.NoError(t, db.Model(&models.Event{}).Where("id = ?", finished.ID).
		Update("end_at", time.Now().Add(-2*time.Hour)).Error)

	r := publicEventsRouter(db)
	w := reqJSON(r, http.MethodGet, "/api/v1/store/mi-tienda/events", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1, "el finalizado no debe aparecer")
	assert.Equal(t, upcoming.ID, resp.Data[0].ID)
}

func TestPublicListEvents_UnknownSlug404(t *testing.T) {
	db, _ := setupPublicEventsDB(t)
	r := publicEventsRouter(db)
	w := reqJSON(r, http.MethodGet, "/api/v1/store/no-existe/events", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestPublicRegisterEvent_RequiresConsent(t *testing.T) {
	db, tenant := setupPublicEventsDB(t)
	ev := seedPublished(t, db, tenant.ID, 100000, 10)
	r := publicEventsRouter(db)

	w := reqJSON(r, http.MethodPost, "/api/v1/store/mi-tienda/events/"+ev.ID+"/register", map[string]any{
		"name":          "Ana",
		"phone":         "3001234567",
		"consent_comms": false,
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPublicRegisterEvent_Succeeds(t *testing.T) {
	db, tenant := setupPublicEventsDB(t)
	ev := seedPublished(t, db, tenant.ID, 100000, 10)
	r := publicEventsRouter(db)

	w := reqJSON(r, http.MethodPost, "/api/v1/store/mi-tienda/events/"+ev.ID+"/register", map[string]any{
		"name":          "Ana",
		"phone":         "3001234567",
		"consent_comms": true,
	})
	require.Equal(t, http.StatusCreated, w.Code)
	var resp struct {
		Data struct {
			PublicToken   string `json:"public_token"`
			PaymentStatus string `json:"payment_status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data.PublicToken)
	assert.Equal(t, models.RegistrationPaymentPending, resp.Data.PaymentStatus)
}

func TestPublicRegisterEvent_Idempotent(t *testing.T) {
	db, tenant := setupPublicEventsDB(t)
	ev := seedPublished(t, db, tenant.ID, 0, 10) // free → confirmed
	r := publicEventsRouter(db)

	body := map[string]any{
		"id":            "33333333-3333-4333-8333-333333333333",
		"name":          "Ana",
		"phone":         "3001234567",
		"consent_comms": true,
	}
	w1 := reqJSON(r, http.MethodPost, "/api/v1/store/mi-tienda/events/"+ev.ID+"/register", body)
	require.Equal(t, http.StatusCreated, w1.Code)
	w2 := reqJSON(r, http.MethodPost, "/api/v1/store/mi-tienda/events/"+ev.ID+"/register", body)
	// Same client UUID must not create a duplicate registration.
	require.Contains(t, []int{http.StatusCreated, http.StatusConflict, http.StatusOK}, w2.Code)

	var n int64
	require.NoError(t, db.Model(&models.EventRegistration{}).Where("event_id = ?", ev.ID).Count(&n).Error)
	assert.Equal(t, int64(1), n)
}

// A paid event registers as pending and MUST NOT leak the carné QR; a free
// event confirms at once and returns the QR (spec FR-09 carné gating).
func TestPublicRegister_GatesCarnetByPayment(t *testing.T) {
	db, tenant := setupPublicEventsDB(t)
	r := publicEventsRouter(db)

	paid := seedPublished(t, db, tenant.ID, 80000, 10)
	wp := reqJSON(r, http.MethodPost, "/api/v1/store/mi-tienda/events/"+paid.ID+"/register", map[string]any{
		"name": "Ana", "phone": "3001234567", "consent_comms": true,
	})
	require.Equal(t, http.StatusCreated, wp.Code)
	var paidResp struct {
		Data struct {
			QRToken   string `json:"qr_token"`
			Confirmed bool   `json:"confirmed"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(wp.Body.Bytes(), &paidResp))
	assert.False(t, paidResp.Data.Confirmed)
	assert.Empty(t, paidResp.Data.QRToken, "evento de pago pendiente NO debe entregar el carné")

	free := seedPublished(t, db, tenant.ID, 0, 10)
	wf := reqJSON(r, http.MethodPost, "/api/v1/store/mi-tienda/events/"+free.ID+"/register", map[string]any{
		"name": "Beto", "phone": "3009999999", "consent_comms": true,
	})
	require.Equal(t, http.StatusCreated, wf.Code)
	var freeResp struct {
		Data struct {
			QRToken   string `json:"qr_token"`
			Confirmed bool   `json:"confirmed"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(wf.Body.Bytes(), &freeResp))
	assert.True(t, freeResp.Data.Confirmed)
	assert.NotEmpty(t, freeResp.Data.QRToken, "evento gratis confirmado SÍ entrega el carné")
}

// The public carné portal reveals the QR only once the balance is cleared.
func TestPublicGetCarnet_RevealsQROnlyWhenPaid(t *testing.T) {
	db, tenant := setupPublicEventsDB(t)
	r := publicEventsRouter(db)
	ev := seedPublished(t, db, tenant.ID, 50000, 10)

	reg := reqJSON(r, http.MethodPost, "/api/v1/store/mi-tienda/events/"+ev.ID+"/register", map[string]any{
		"name": "Ana", "phone": "3001234567", "consent_comms": true,
	})
	var regResp struct {
		Data struct {
			PublicToken string `json:"public_token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(reg.Body.Bytes(), &regResp))
	token := regResp.Data.PublicToken
	require.NotEmpty(t, token)

	// Pendiente → sin QR, con saldo.
	w1 := reqJSON(r, http.MethodGet, "/api/v1/store/mi-tienda/carnet/"+token, nil)
	require.Equal(t, http.StatusOK, w1.Code)
	var c1 struct {
		Data struct {
			Confirmed bool   `json:"confirmed"`
			Balance   int64  `json:"balance"`
			QRToken   string `json:"qr_token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &c1))
	assert.False(t, c1.Data.Confirmed)
	assert.Equal(t, int64(50000), c1.Data.Balance)
	assert.Empty(t, c1.Data.QRToken)

	// Pago completo → carné con QR.
	var dbReg models.EventRegistration
	require.NoError(t, db.Where("public_token = ?", token).First(&dbReg).Error)
	_, err := services.NewEventRegistrationService(db).ConfirmPayment(tenant.ID, dbReg.ID)
	require.NoError(t, err)

	w2 := reqJSON(r, http.MethodGet, "/api/v1/store/mi-tienda/carnet/"+token, nil)
	var c2 struct {
		Data struct {
			Confirmed bool   `json:"confirmed"`
			Balance   int64  `json:"balance"`
			QRToken   string `json:"qr_token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &c2))
	assert.True(t, c2.Data.Confirmed)
	assert.Equal(t, int64(0), c2.Data.Balance)
	assert.NotEmpty(t, c2.Data.QRToken)
}

// A guest submits a manual-payment proof; the organizer approves it and the
// carné activates once the balance clears (decision: manual con comprobante).
func TestSubmitProof_ThenApprove_ActivatesCarnet(t *testing.T) {
	db, tenant := setupPublicEventsDB(t)
	pub := publicEventsRouter(db)
	pub.POST("/api/v1/store/:slug/carnet/:token/proof", PublicSubmitPaymentProof(db, nil))
	ev := seedPublished(t, db, tenant.ID, 60000, 10)

	reg := reqJSON(pub, http.MethodPost, "/api/v1/store/mi-tienda/events/"+ev.ID+"/register", map[string]any{
		"name": "Ana", "phone": "3001234567", "consent_comms": true,
	})
	var regResp struct {
		Data struct {
			PublicToken string `json:"public_token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(reg.Body.Bytes(), &regResp))
	token := regResp.Data.PublicToken

	// El invitado reporta el pago (sin imagen, solo monto) → queda pendiente.
	form := "amount=60000&note=Transferencia+Nequi"
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/store/mi-tienda/carnet/"+token+"/proof", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())

	// El organizador ve el comprobante pendiente y lo aprueba.
	org := eventsRouter(db, tenant.ID, "admin")
	org.GET("/api/v1/events/:id/payments", ListEventPayments(db))
	org.POST("/api/v1/events/:id/payments/:pid/approve", ApproveEventPayment(db))

	list := reqJSON(org, http.MethodGet, "/api/v1/events/"+ev.ID+"/payments?status=pending", nil)
	require.Equal(t, http.StatusOK, list.Code)
	var listResp struct {
		Data []struct {
			ID     string `json:"id"`
			Amount int64  `json:"amount"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &listResp))
	require.Len(t, listResp.Data, 1)
	assert.Equal(t, int64(60000), listResp.Data[0].Amount)

	approve := reqJSON(org, http.MethodPost,
		"/api/v1/events/"+ev.ID+"/payments/"+listResp.Data[0].ID+"/approve", nil)
	require.Equal(t, http.StatusOK, approve.Code)
	var appResp struct {
		Data models.EventRegistration `json:"data"`
	}
	require.NoError(t, json.Unmarshal(approve.Body.Bytes(), &appResp))
	assert.Equal(t, models.RegistrationPaymentConfirmed, appResp.Data.PaymentStatus)
	assert.Equal(t, int64(60000), appResp.Data.AmountPaid)
}

// Registrar con email lo guarda en el cliente; el carné expone ubicación.
func TestPublicRegister_CapturesEmailAndCarnetLocation(t *testing.T) {
	db, tenant := setupPublicEventsDB(t)
	r := publicEventsRouter(db)

	// Evento presencial con ubicación estructurada.
	ev, err := services.NewEventService(db).Create(tenant.ID, &models.Event{
		Type: models.EventTypeCurso, Title: "Curso", Modality: models.EventModalityPresencial,
		Capacity: 10, Price: 50000,
		LocationOrLink: "Calle 8 #28-14", City: "Medellín", LocationNotes: "Edificio Norte, piso 3",
	})
	require.NoError(t, err)
	_, err = services.NewEventService(db).Publish(tenant.ID, ev.ID)
	require.NoError(t, err)

	reg := reqJSON(r, http.MethodPost, "/api/v1/store/mi-tienda/events/"+ev.ID+"/register", map[string]any{
		"name": "Ana", "phone": "3001234567", "email": "ana@correo.com", "consent_comms": true,
	})
	require.Equal(t, http.StatusCreated, reg.Code)
	var regResp struct {
		Data struct {
			PublicToken string `json:"public_token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(reg.Body.Bytes(), &regResp))

	// El email quedó en el cliente.
	var c models.Customer
	require.NoError(t, db.Where("tenant_id = ? AND phone = ?", tenant.ID, "3001234567").First(&c).Error)
	assert.Equal(t, "ana@correo.com", c.Email)

	// El carné expone la ubicación estructurada + el nombre.
	w := reqJSON(r, http.MethodGet, "/api/v1/store/mi-tienda/carnet/"+regResp.Data.PublicToken, nil)
	require.Equal(t, http.StatusOK, w.Code)
	var carnet struct {
		Data struct {
			City          string `json:"city"`
			Location      string `json:"location"`
			LocationNotes string `json:"location_notes"`
			AttendeeName  string `json:"attendee_name"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &carnet))
	assert.Equal(t, "Medellín", carnet.Data.City)
	assert.Equal(t, "Calle 8 #28-14", carnet.Data.Location)
	assert.Equal(t, "Edificio Norte, piso 3", carnet.Data.LocationNotes)
	assert.Equal(t, "Ana", carnet.Data.AttendeeName)
}

// El lookup por teléfono recupera la inscripción (prefiere la pendiente).
func TestPublicFindRegistration_ByPhone(t *testing.T) {
	db, tenant := setupPublicEventsDB(t)
	r := publicEventsRouter(db)
	ev := seedPublished(t, db, tenant.ID, 50000, 10)

	reg := reqJSON(r, http.MethodPost, "/api/v1/store/mi-tienda/events/"+ev.ID+"/register", map[string]any{
		"name": "Ana", "phone": "3001234567", "consent_comms": true,
	})
	require.Equal(t, http.StatusCreated, reg.Code)
	var regResp struct {
		Data struct {
			PublicToken string `json:"public_token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(reg.Body.Bytes(), &regResp))

	// Encuentra por teléfono.
	w := reqJSON(r, http.MethodPost, "/api/v1/store/mi-tienda/my-event-registration",
		map[string]any{"phone": "300 123 4567"})
	require.Equal(t, http.StatusOK, w.Code)
	var found struct {
		Data struct {
			PublicToken string `json:"public_token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &found))
	assert.Equal(t, regResp.Data.PublicToken, found.Data.PublicToken)

	// Teléfono desconocido → 404.
	w2 := reqJSON(r, http.MethodPost, "/api/v1/store/mi-tienda/my-event-registration",
		map[string]any{"phone": "3009999999"})
	assert.Equal(t, http.StatusNotFound, w2.Code)
}

// El carné confirmado entrega el badge config (layout + textos) cuando el
// organizador diseñó la escarapela en el editor WYSIWYG.
func TestPublicGetCarnet_IncludesBadgeConfig(t *testing.T) {
	db, tenant := setupPublicEventsDB(t)
	r := publicEventsRouter(db)
	ev := seedPublished(t, db, tenant.ID, 50000, 10)
	// Re-fetch para no pisar el status con la copia stale de seedPublished.
	fresh, gerr := services.NewEventService(db).Get(tenant.ID, ev.ID)
	require.NoError(t, gerr)
	fresh.BadgeConfig = models.EventCertificateConfig{
		Layout: map[string]models.CertElementPos{
			"name": {X: 0.5, Y: 0.42, Scale: 0.08},
			"qr":   {X: 0.5, Y: 0.71, Scale: 0.4},
		},
	}
	require.NoError(t, db.Save(fresh).Error)

	reg := reqJSON(r, http.MethodPost, "/api/v1/store/mi-tienda/events/"+ev.ID+"/register", map[string]any{
		"name": "Vivi", "phone": "3001234567", "consent_comms": true,
	})
	var regResp struct {
		Data struct {
			PublicToken string `json:"public_token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(reg.Body.Bytes(), &regResp))

	var dbReg models.EventRegistration
	require.NoError(t, db.Where("public_token = ?", regResp.Data.PublicToken).First(&dbReg).Error)
	_, err := services.NewEventRegistrationService(db).ConfirmPayment(tenant.ID, dbReg.ID)
	require.NoError(t, err)

	w := reqJSON(r, http.MethodGet, "/api/v1/store/mi-tienda/carnet/"+regResp.Data.PublicToken, nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Badge map[string]any `json:"badge"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data.Badge, "el carné confirmado debe traer el badge config")
	assert.Equal(t, "Vivi", resp.Data.Badge["attendee_name"])
	assert.NotNil(t, resp.Data.Badge["layout"])
	assert.Contains(t, resp.Data.Badge, "intro") // organizador por defecto
}

// Sin badge config (layout vacío), el carné NO trae 'badge' → el front usa el
// overlay por defecto (retrocompatible).
func TestPublicGetCarnet_NoBadgeConfig_OmitsBadge(t *testing.T) {
	db, tenant := setupPublicEventsDB(t)
	r := publicEventsRouter(db)
	ev := seedPublished(t, db, tenant.ID, 0, 10) // gratis → confirmado al inscribir

	reg := reqJSON(r, http.MethodPost, "/api/v1/store/mi-tienda/events/"+ev.ID+"/register", map[string]any{
		"name": "Leo", "phone": "3009998888", "consent_comms": true,
	})
	var regResp struct {
		Data struct {
			PublicToken string `json:"public_token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(reg.Body.Bytes(), &regResp))

	w := reqJSON(r, http.MethodGet, "/api/v1/store/mi-tienda/carnet/"+regResp.Data.PublicToken, nil)
	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	_, hasBadge := resp.Data["badge"]
	assert.False(t, hasBadge, "sin layout no debe venir 'badge'")
}
