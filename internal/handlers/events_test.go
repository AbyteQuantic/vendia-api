// Spec: specs/042-modulo-eventos/spec.md
package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
)

func setupEventsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Event{}, &models.EventRegistration{}, &models.EventScan{}, &models.EventPayment{},
	))
	return db
}

// eventsRouter mounts the authed event routes with an injected actor.
func eventsRouter(db *gorm.DB, tenantID, role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Set(middleware.RoleKey, role)
		c.Set(middleware.UserIDKey, "user-1")
		c.Next()
	})
	g := r.Group("/api/v1")
	g.POST("/events", CreateEvent(db))
	g.GET("/events", ListEvents(db))
	g.GET("/events/:id", GetEvent(db))
	g.PATCH("/events/:id", UpdateEvent(db))
	g.DELETE("/events/:id", DeleteEvent(db))
	g.POST("/events/:id/publish", PublishEvent(db))
	g.POST("/events/:id/checkin", CheckinEvent(db))
	g.POST("/events/:id/registrations/:rid/payments", RecordRegistrationPayment(db))
	g.POST("/events/:id/registrations/:rid/confirm-payment", ConfirmRegistrationPayment(db))
	// AI generators with a nil Gemini service to assert the guard path.
	g.POST("/events/:id/badge/ai-generate", GenerateEventBadgeImage(db, nil, nil))
	g.POST("/events/:id/poster/ai-generate", GenerateEventPosterImage(db, nil, nil))
	// "Sube tu propia imagen" — con almacenamiento falso para el camino feliz.
	g.POST("/events/:id/poster/upload", UploadEventPosterImage(db, newFakeStorage()))
	return r
}

func validEventBody() map[string]any {
	return map[string]any{
		"type":     models.EventTypeConferencia,
		"title":    "Conferencia de barrio",
		"modality": models.EventModalityPresencial,
		"capacity": 50,
		"price":    80000,
	}
}

func TestCreateEvent_AdminSucceeds(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "admin")

	w := reqJSON(r, http.MethodPost, "/api/v1/events", validEventBody())
	require.Equal(t, http.StatusCreated, w.Code)

	var resp struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data.ID)
	assert.Equal(t, "tenant-a", resp.Data.TenantID)
	assert.Equal(t, models.EventStatusBorrador, resp.Data.Status)
}

func TestCreateEvent_StaffAllowed(t *testing.T) {
	// El JWT lleva el WorkspaceRole: el dueño es "owner", también admin/cashier.
	for _, role := range []string{"owner", "admin", "cashier"} {
		t.Run(role, func(t *testing.T) {
			db := setupEventsDB(t)
			r := eventsRouter(db, "tenant-a", role)
			w := reqJSON(r, http.MethodPost, "/api/v1/events", validEventBody())
			assert.Equal(t, http.StatusCreated, w.Code, "rol %s debe poder crear", role)
		})
	}
}

func TestCreateEvent_NoRoleForbidden(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "")

	w := reqJSON(r, http.MethodPost, "/api/v1/events", validEventBody())
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestCreateEvent_PersistsStartAt(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "admin")

	body := validEventBody()
	body["start_at"] = "2026-07-20T15:00:00Z"
	w := reqJSON(r, http.MethodPost, "/api/v1/events", body)
	require.Equal(t, http.StatusCreated, w.Code)

	var resp struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data.StartAt, "la fecha del evento debe persistirse")
	assert.Equal(t, 2026, resp.Data.StartAt.Year())
	assert.Equal(t, 7, int(resp.Data.StartAt.Month()))
	assert.Equal(t, 20, resp.Data.StartAt.Day())
}

func TestCreateEvent_RejectsInvalidPrice(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "admin")

	body := validEventBody()
	body["price"] = 80025 // not a multiple of $50
	w := reqJSON(r, http.MethodPost, "/api/v1/events", body)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListAndGetEvent_TenantScoped(t *testing.T) {
	db := setupEventsDB(t)
	rA := eventsRouter(db, "tenant-a", "admin")

	create := reqJSON(rA, http.MethodPost, "/api/v1/events", validEventBody())
	require.Equal(t, http.StatusCreated, create.Code)
	var created struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &created))

	// tenant-b cannot read tenant-a's event nor see it in the list.
	rB := eventsRouter(db, "tenant-b", "admin")
	get := reqJSON(rB, http.MethodGet, "/api/v1/events/"+created.Data.ID, nil)
	assert.Equal(t, http.StatusNotFound, get.Code)

	list := reqJSON(rB, http.MethodGet, "/api/v1/events", nil)
	require.Equal(t, http.StatusOK, list.Code)
	var listResp struct {
		Data []models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &listResp))
	assert.Empty(t, listResp.Data)
}

func TestPublishEvent(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "admin")

	create := reqJSON(r, http.MethodPost, "/api/v1/events", validEventBody())
	require.Equal(t, http.StatusCreated, create.Code)
	var created struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &created))

	pub := reqJSON(r, http.MethodPost, "/api/v1/events/"+created.Data.ID+"/publish", nil)
	require.Equal(t, http.StatusOK, pub.Code)
	var pubResp struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(pub.Body.Bytes(), &pubResp))
	assert.Equal(t, models.EventStatusPublicado, pubResp.Data.Status)
}

func TestCheckinEvent_Idempotent(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "admin")

	create := reqJSON(r, http.MethodPost, "/api/v1/events", validEventBody())
	var created struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &created))

	qr := "44444444-4444-4444-8444-444444444444"
	require.NoError(t, db.Create(&models.EventRegistration{
		TenantID: "tenant-a", EventID: created.Data.ID, CustomerID: "c1",
		QRToken: qr, PublicToken: "55555555-5555-4555-8555-555555555555",
		PaymentStatus: models.RegistrationPaymentConfirmed,
	}).Error)

	body := map[string]any{"qr_token": qr, "scan_type": models.ScanTypeIn}
	w1 := reqJSON(r, http.MethodPost, "/api/v1/events/"+created.Data.ID+"/checkin", body)
	require.Equal(t, http.StatusOK, w1.Code)
	var resp1 struct {
		AlreadyRegistered bool `json:"already_registered"`
	}
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &resp1))
	assert.False(t, resp1.AlreadyRegistered)

	w2 := reqJSON(r, http.MethodPost, "/api/v1/events/"+created.Data.ID+"/checkin", body)
	require.Equal(t, http.StatusOK, w2.Code)
	var resp2 struct {
		AlreadyRegistered bool `json:"already_registered"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp2))
	assert.True(t, resp2.AlreadyRegistered)
}

// seedRegistration inserts a pending paid-event registration for payment tests.
func seedRegistration(t *testing.T, db *gorm.DB, tenantID, eventID, qr string) string {
	t.Helper()
	reg := models.EventRegistration{
		TenantID: tenantID, EventID: eventID, CustomerID: "cust-1",
		QRToken:       qr,
		PublicToken:   "66666666-6666-4666-8666-666666666666",
		PaymentStatus: models.RegistrationPaymentPending,
	}
	require.NoError(t, db.Create(&reg).Error)
	return reg.ID
}

func TestRecordPayment_PartialThenFullConfirms(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "admin")

	create := reqJSON(r, http.MethodPost, "/api/v1/events", validEventBody()) // price 80000
	var created struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &created))
	rid := seedRegistration(t, db, "tenant-a", created.Data.ID, "77777777-7777-4777-8777-777777777777")

	base := "/api/v1/events/" + created.Data.ID + "/registrations/" + rid
	// Primer abono: queda pendiente con saldo.
	w1 := reqJSON(r, http.MethodPost, base+"/payments", map[string]any{"amount": 40000})
	require.Equal(t, http.StatusOK, w1.Code)
	var resp1 struct {
		Data models.EventRegistration `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &resp1))
	assert.Equal(t, models.RegistrationPaymentPending, resp1.Data.PaymentStatus)
	assert.Equal(t, int64(40000), resp1.Data.AmountPaid)

	// Segundo abono completa el precio → confirmado.
	w2 := reqJSON(r, http.MethodPost, base+"/payments", map[string]any{"amount": 40000})
	require.Equal(t, http.StatusOK, w2.Code)
	var resp2 struct {
		Data models.EventRegistration `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp2))
	assert.Equal(t, models.RegistrationPaymentConfirmed, resp2.Data.PaymentStatus)
	assert.Equal(t, int64(80000), resp2.Data.AmountPaid)
}

func TestCheckin_RejectsUnpaidCarnet(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "admin")

	create := reqJSON(r, http.MethodPost, "/api/v1/events", validEventBody())
	var created struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &created))
	qr := "88888888-8888-4888-8888-888888888888"
	seedRegistration(t, db, "tenant-a", created.Data.ID, qr)

	// Carné sin pagar → el escaneo se rechaza (400, no válido).
	body := map[string]any{"qr_token": qr, "scan_type": models.ScanTypeIn}
	w := reqJSON(r, http.MethodPost, "/api/v1/events/"+created.Data.ID+"/checkin", body)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Tras confirmar el pago, el mismo carné ya entra.
	rid := ""
	var reg models.EventRegistration
	require.NoError(t, db.Where("qr_token = ?", qr).First(&reg).Error)
	rid = reg.ID
	confirm := reqJSON(r, http.MethodPost,
		"/api/v1/events/"+created.Data.ID+"/registrations/"+rid+"/confirm-payment", nil)
	require.Equal(t, http.StatusOK, confirm.Code)

	w2 := reqJSON(r, http.MethodPost, "/api/v1/events/"+created.Data.ID+"/checkin", body)
	assert.Equal(t, http.StatusOK, w2.Code)
}

func TestRecordPayment_RejectsNonPositive(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "admin")
	create := reqJSON(r, http.MethodPost, "/api/v1/events", validEventBody())
	var created struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &created))
	rid := seedRegistration(t, db, "tenant-a", created.Data.ID, "99999999-9999-4999-8999-999999999999")

	w := reqJSON(r, http.MethodPost,
		"/api/v1/events/"+created.Data.ID+"/registrations/"+rid+"/payments",
		map[string]any{"amount": -10})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGenerateEventBadge_RequiresAIService(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "admin")

	create := reqJSON(r, http.MethodPost, "/api/v1/events", validEventBody())
	var created struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &created))

	// With a nil Gemini service the endpoint must degrade to 503, not panic.
	w := reqJSON(r, http.MethodPost, "/api/v1/events/"+created.Data.ID+"/badge/ai-generate", nil)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestGenerateEventBadge_NoRoleForbidden(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "")
	w := reqJSON(r, http.MethodPost, "/api/v1/events/whatever/badge/ai-generate", nil)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestGenerateEventPoster_RequiresAIService(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "admin")

	create := reqJSON(r, http.MethodPost, "/api/v1/events", validEventBody())
	var created struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &created))

	// Nil Gemini service → 503, never a panic (mirrors the badge guard).
	w := reqJSON(r, http.MethodPost, "/api/v1/events/"+created.Data.ID+"/poster/ai-generate", nil)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestGenerateEventPoster_NoRoleForbidden(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "")
	w := reqJSON(r, http.MethodPost, "/api/v1/events/whatever/poster/ai-generate", nil)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestUploadEventPoster_HappyPath(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "admin")

	create := reqJSON(r, http.MethodPost, "/api/v1/events", validEventBody())
	var created struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &created))

	body, ctype := buildMultipartQR(t, "image", "afiche.png", "image/png", pngBytes)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/events/"+created.Data.ID+"/poster/upload", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var resp struct {
		Data struct {
			ImageURL string `json:"image_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data.ImageURL)

	// La URL del afiche quedó persistida en la plantilla del evento.
	var ev models.Event
	require.NoError(t, db.Where("id = ?", created.Data.ID).First(&ev).Error)
	assert.Equal(t, resp.Data.ImageURL, ev.PosterTemplate.ImageURL)
}

func TestUploadEventPoster_RejectsNonImage(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "admin")

	create := reqJSON(r, http.MethodPost, "/api/v1/events", validEventBody())
	var created struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &created))

	body, ctype := buildMultipartQR(t, "image", "nota.txt", "text/plain", []byte("hola"))
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/events/"+created.Data.ID+"/poster/upload", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUploadEventPoster_NoRoleForbidden(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "")
	body, ctype := buildMultipartQR(t, "image", "afiche.png", "image/png", pngBytes)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/events/whatever/poster/upload", body)
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestDeleteEvent_Archives(t *testing.T) {
	db := setupEventsDB(t)
	r := eventsRouter(db, "tenant-a", "admin")

	create := reqJSON(r, http.MethodPost, "/api/v1/events", validEventBody())
	var created struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(create.Body.Bytes(), &created))

	del := reqJSON(r, http.MethodDelete, "/api/v1/events/"+created.Data.ID, nil)
	assert.Equal(t, http.StatusOK, del.Code)

	get := reqJSON(r, http.MethodGet, "/api/v1/events/"+created.Data.ID, nil)
	require.Equal(t, http.StatusOK, get.Code)
	var getResp struct {
		Data models.Event `json:"data"`
	}
	require.NoError(t, json.Unmarshal(get.Body.Bytes(), &getResp))
	assert.Equal(t, models.EventStatusArchivado, getResp.Data.Status)
}
