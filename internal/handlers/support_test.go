package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupSupportDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	db.AutoMigrate(&models.Tenant{}, &models.User{}, &models.SupportTicket{}, &models.SupportTicketMessage{})
	return db
}

func mountTenantSupport(db *gorm.DB, tenantID, userID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if tenantID != "" {
			c.Set(middleware.TenantIDKey, tenantID)
		}
		if userID != "" {
			c.Set(middleware.UserIDKey, userID)
		}
		c.Next()
	})
	r.POST("/api/v1/support/tickets", handlers.CreateSupportTicket(db))
	r.GET("/api/v1/support/tickets", handlers.ListTenantTickets(db))
	r.GET("/api/v1/support/tickets/:id", handlers.GetTenantTicket(db))
	return r
}

func TestCreateSupportTicket_PersistsTicketAndInitialMessage(t *testing.T) {
	db := setupSupportDB(t)
	tenantID := uuid.NewString()
	userID := uuid.NewString()
	r := mountTenantSupport(db, tenantID, userID)

	b, _ := json.Marshal(map[string]any{
		"subject": "Ayuda",
		"message": "Mensaje inicial",
		"category": "BUG",
	})
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/support/tickets", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var ticket models.SupportTicket
	err := db.Preload("Messages").First(&ticket).Error
	require.NoError(t, err)
	assert.Equal(t, "Ayuda", ticket.Subject)
	assert.Equal(t, "BUG", ticket.Category)
	
	var messages []models.SupportTicketMessage
	db.Where("ticket_id = ?", ticket.ID).Find(&messages)
	require.Len(t, messages, 1)
	assert.Equal(t, "Mensaje inicial", messages[0].Content)
}

func TestAdminListSupportTickets_Ordering(t *testing.T) {
	db := setupSupportDB(t)
	
	t1ID := uuid.NewString()
	t1 := models.Tenant{BaseModel: models.BaseModel{ID: t1ID}, BusinessName: "T1"}
	db.Create(&t1)

	// Ticket 1: Resolved
	tk1 := models.SupportTicket{
		TenantID: t1ID,
		Subject: "Resolved",
		Status: "RESOLVED",
	}
	tk1.ID = uuid.NewString()
	db.Create(&tk1)

	// Ticket 2: Open
	tk2 := models.SupportTicket{
		TenantID: t1ID,
		Subject: "Open",
		Status: "OPEN",
	}
	tk2.ID = uuid.NewString()
	db.Create(&tk2)

	r := gin.New()
	r.GET("/admin/tickets", handlers.AdminListSupportTickets(db))

	req, _ := http.NewRequest(http.MethodGet, "/admin/tickets", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var res []handlers.AdminTicketRow
	json.Unmarshal(w.Body.Bytes(), &res)

	require.Len(t, res, 2)
	assert.Equal(t, tk2.ID, res[0].ID) // Open first
	assert.Equal(t, tk1.ID, res[1].ID) // Resolved last
}

func TestAdminAddMessage_UpdatesStatusToInProgress(t *testing.T) {
	db := setupSupportDB(t)
	
	t1ID := uuid.NewString()
	t1 := models.Tenant{BaseModel: models.BaseModel{ID: t1ID}, BusinessName: "T1"}
	db.Create(&t1)

	tk := models.SupportTicket{
		TenantID: t1ID,
		Status: "OPEN",
	}
	tk.ID = uuid.NewString()
	db.Create(&tk)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.UserIDKey, uuid.NewString())
		c.Next()
	})
	r.POST("/admin/tickets/:id/messages", handlers.AdminAddTicketMessage(db))

	b, _ := json.Marshal(map[string]any{"content": "Respuesta admin"})
	req, _ := http.NewRequest(http.MethodPost, "/admin/tickets/"+tk.ID+"/messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var updated models.SupportTicket
	db.First(&updated, "id = ?", tk.ID)
	assert.Equal(t, "IN_PROGRESS", updated.Status)
}
