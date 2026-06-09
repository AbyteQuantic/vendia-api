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

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
)

func setupEventsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Event{}, &models.EventRegistration{}, &models.EventScan{},
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
	// AI generators with a nil Gemini service to assert the guard path.
	g.POST("/events/:id/badge/ai-generate", GenerateEventBadgeImage(db, nil, nil))
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
