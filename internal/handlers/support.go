package handlers

import (
	"net/http"
	"strings"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ── Request Types ────────────────────────────────────────────────────────────

type createTicketRequest struct {
	Subject  string `json:"subject" binding:"required"`
	Message  string `json:"message" binding:"required"`
	Category string `json:"category"`
	Priority string `json:"priority"`
}

type addMessageRequest struct {
	Content string `json:"content" binding:"required"`
}

type updateTicketRequest struct {
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

// ── Admin Types ─────────────────────────────────────────────────────────────

type AdminTicketRow struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	BusinessName string    `json:"business_name"`
	Subject      string    `json:"subject"`
	Status       string    `json:"status"`
	Priority     string    `json:"priority"`
	Category     string    `json:"category"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	LastMessage  string    `json:"last_message"`
}

// ── Handlers (Tenant) ────────────────────────────────────────────────────────

func CreateSupportTicket(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserIDPtr(c)

		var req createTicketRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "asunto y mensaje son obligatorios"})
			return
		}

		priority := req.Priority
		if _, ok := models.ValidTicketPriorities[priority]; !ok {
			priority = models.TicketPriorityNormal
		}

		category := req.Category
		if _, ok := models.ValidTicketCategories[category]; !ok {
			category = models.TicketCategoryOther
		}

		ticket := models.SupportTicket{
			TenantID: tenantID,
			UserID:   userID,
			Subject:  strings.TrimSpace(req.Subject),
			Status:   models.TicketStatusOpen,
			Priority: priority,
			Category: category,
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&ticket).Error; err != nil {
				return err
			}
			// Initial message
			msg := models.SupportTicketMessage{
				TicketID:   ticket.ID,
				SenderType: "TENANT",
				SenderID:   tenantID, // Or userID if we prefer
				Content:    strings.TrimSpace(req.Message),
			}
			return tx.Create(&msg).Error
		})

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear ticket"})
			return
		}

		c.JSON(http.StatusCreated, ticket)
	}
}

func ListTenantTickets(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var tickets []models.SupportTicket
		if err := db.Where("tenant_id = ?", tenantID).Order("updated_at desc").Find(&tickets).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al listar tickets"})
			return
		}
		c.JSON(http.StatusOK, tickets)
	}
}

func GetTenantTicket(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		id := c.Param("id")

		var ticket models.SupportTicket
		if err := db.Preload("Messages", func(db *gorm.DB) *gorm.DB {
			return db.Order("created_at asc")
		}).Where("id = ? AND tenant_id = ?", id, tenantID).First(&ticket).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "ticket no encontrado"})
			return
		}
		c.JSON(http.StatusOK, ticket)
	}
}

func AddTenantMessage(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		id := c.Param("id")

		var req addMessageRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "contenido es requerido"})
			return
		}

		// Verify ownership
		var ticket models.SupportTicket
		if err := db.Where("id = ? AND tenant_id = ?", id, tenantID).First(&ticket).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "ticket no encontrado"})
			return
		}

		msg := models.SupportTicketMessage{
			TicketID:   id,
			SenderType: "TENANT",
			SenderID:   tenantID,
			Content:    strings.TrimSpace(req.Content),
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&msg).Error; err != nil {
				return err
			}
			// Reopen if resolved? Or just update updated_at
			return tx.Model(&ticket).Update("updated_at", time.Now()).Error
		})

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al enviar mensaje"})
			return
		}

		c.JSON(http.StatusCreated, msg)
	}
}

// ── Handlers (Admin) ────────────────────────────────────────────────────────

func AdminListSupportTickets(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		status := c.Query("status")
		
		var rows []AdminTicketRow
		query := db.Table("support_tickets AS st").
			Select(`st.id, st.tenant_id, t.business_name, st.subject, st.status, st.priority, st.category, st.created_at, st.updated_at,
			        COALESCE((SELECT content FROM support_ticket_messages WHERE ticket_id = st.id ORDER BY created_at DESC LIMIT 1), '') as last_message`).
			Joins("JOIN tenants t ON t.id = st.tenant_id")
		
		if status != "" {
			query = query.Where("st.status = ?", status)
		}

		err := query.Order("CASE WHEN st.status = 'OPEN' THEN 0 WHEN st.status = 'IN_PROGRESS' THEN 1 ELSE 2 END, st.updated_at DESC").
			Scan(&rows).Error

		if err != nil {
			// CRITICAL: Log the actual error for production debugging
			log.Printf("[SUPPORT_API] list tickets failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener tickets"})
			return
		}
		c.JSON(http.StatusOK, rows)
	}
}

func AdminGetSupportTicket(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var ticket models.SupportTicket
		if err := db.Preload("Messages", func(db *gorm.DB) *gorm.DB {
			return db.Order("created_at asc")
		}).First(&ticket, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "ticket no encontrado"})
			return
		}

		// Get business name
		var tenant models.Tenant
		db.Select("business_name").First(&tenant, "id = ?", ticket.TenantID)
		ticket.BusinessName = tenant.BusinessName

		c.JSON(http.StatusOK, ticket)
	}
}

func AdminAddTicketMessage(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		adminID := middleware.GetUserID(c) // Assumes super-admin user id

		var req addMessageRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "contenido es requerido"})
			return
		}

		msg := models.SupportTicketMessage{
			TicketID:   id,
			SenderType: "ADMIN",
			SenderID:   adminID,
			Content:    strings.TrimSpace(req.Content),
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&msg).Error; err != nil {
				return err
			}
			// Automatically mark as IN_PROGRESS if it was OPEN
			return tx.Model(&models.SupportTicket{}).
				Where("id = ? AND status = ?", id, models.TicketStatusOpen).
				Updates(map[string]interface{}{
					"status":     models.TicketStatusInProgress,
					"updated_at": time.Now(),
				}).Error
		})

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al enviar respuesta"})
			return
		}

		c.JSON(http.StatusCreated, msg)
	}
}

func AdminUpdateSupportTicket(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var req updateTicketRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}

		updates := make(map[string]interface{})
		if req.Status != "" {
			if _, ok := models.ValidTicketStatuses[req.Status]; ok {
				updates["status"] = req.Status
			}
		}
		if req.Priority != "" {
			if _, ok := models.ValidTicketPriorities[req.Priority]; ok {
				updates["priority"] = req.Priority
			}
		}

		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "nada que actualizar"})
			return
		}

		updates["updated_at"] = time.Now()

		if err := db.Model(&models.SupportTicket{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar ticket"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ticket actualizado"})
	}
}
