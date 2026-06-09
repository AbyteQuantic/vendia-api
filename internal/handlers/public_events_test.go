// Spec: specs/042-modulo-eventos/spec.md
package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

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
		&models.EventScan{}, &models.Customer{},
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
