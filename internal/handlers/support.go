package handlers

import (
	"net/http"
	"strings"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ── Types ────────────────────────────────────────────────────────────────────

type createTicketRequest struct {
	Subject string `json:"subject" binding:"required"`
	Message string `json:"message" binding:"required"`
}

// AdminTicketRow is the join of support_tickets with just enough
// tenant + user context to render the admin card (business name,
// tenant phone, reporter user phone). Kept flat so the frontend
// doesn't need to thread three different endpoints.
type AdminTicketRow struct {
	ID             string `json:"id"`
	TenantID       string `json:"tenant_id"`
	BusinessName   string `json:"business_name"`
	TenantPhone    string `json:"tenant_phone"`
	UserID         string `json:"user_id,omitempty"`
	UserPhone      string `json:"user_phone,omitempty"`
	Subject        string `json:"subject"`
	Message        string `json:"message"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type updateTicketRequest struct {
	Status string `json:"status" binding:"required"`
}

// ── Handlers ─────────────────────────────────────────────────────────────────

// CreateSupportTicket is mounted under /api/v1/support (tenant-auth).
// Subject is clipped to 160 chars; message is stored as-is (the DB
// column is TEXT). Trimming whitespace stops "      " from passing
// the binding:"required" check.
func CreateSupportTicket(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if tenantID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "sesión requerida"})
			return
		}
		userID := middleware.GetUserIDPtr(c)

		var req createTicketRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "asunto y mensaje son obligatorios",
				"error_code": "invalid_request",
			})
			return
		}
		subject := strings.TrimSpace(req.Subject)
		message := strings.TrimSpace(req.Message)
		if subject == "" || message == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "asunto y mensaje no pueden estar vacíos",
				"error_code": "invalid_request",
			})
			return
		}
		if len(subject) > 160 {
			subject = subject[:160]
		}

		ticket := models.SupportTicket{
			TenantID: tenantID,
			UserID:   userID,
			Subject:  subject,
			Message:  message,
			Status:   models.TicketStatusOpen,
		}
		if err := db.Create(&ticket).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo registrar el ticket",
			})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": ticket})
	}
}

// AdminListSupportTickets returns every ticket sorted so unresolved
// work surfaces first (ORDER BY CASE status THEN created_at DESC).
// Joins tenants + users to avoid N+1 on the frontend.
func AdminListSupportTickets(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var rows []AdminTicketRow
		err := db.Table("support_tickets AS st").
			Select(`st.id                         AS id,
			        st.tenant_id                  AS tenant_id,
			        t.business_name               AS business_name,
			        t.phone                       AS tenant_phone,
			        st.user_id                    AS user_id,
			        COALESCE(u.phone,'')          AS user_phone,
			        st.subject                    AS subject,
			        st.message                    AS message,
			        st.status                     AS status,
			        st.created_at                 AS created_at,
			        st.updated_at                 AS updated_at`).
			Joins("JOIN tenants t ON t.id = st.tenant_id").
			Joins("LEFT JOIN users u ON u.id = st.user_id").
			// CASE expression keeps OPEN rows first without a second query
			Order("CASE WHEN st.status = 'OPEN' THEN 0 ELSE 1 END, st.created_at DESC").
			Scan(&rows).Error
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "error al obtener tickets",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": rows})
	}
}

// AdminUpdateSupportTicket flips a ticket to RESOLVED (or back to
// OPEN if the ops team reopens one). The status value is whitelisted
// via models.ValidTicketStatuses — defence-in-depth before the DB
// CHECK constraint rejects anything else.
func AdminUpdateSupportTicket(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		ticketID := c.Param("id")
		var req updateTicketRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "status requerido",
				"error_code": "invalid_request",
			})
			return
		}
		if _, ok := models.ValidTicketStatuses[req.Status]; !ok {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "status inválido",
				"error_code": "invalid_status",
			})
			return
		}

		result := db.Model(&models.SupportTicket{}).
			Where("id = ?", ticketID).
			Update("status", req.Status)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "no se pudo actualizar el ticket",
			})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "ticket no encontrado"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ticket actualizado"})
	}
}

// SortAdminTicketRowsOpenFirst is an exported helper so tests can
// assert on the exact ordering invariant without a DB. Stable sort
// on (status==OPEN first, then created_at DESC).
func SortAdminTicketRowsOpenFirst(rows []AdminTicketRow) []AdminTicketRow {
	out := make([]AdminTicketRow, len(rows))
	copy(out, rows)
	// simple insertion-sort is fine — ticket lists are O(hundreds)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			if lessTicket(out[j], out[j-1]) {
				out[j], out[j-1] = out[j-1], out[j]
			} else {
				break
			}
		}
	}
	return out
}

func lessTicket(a, b AdminTicketRow) bool {
	aOpen := a.Status == models.TicketStatusOpen
	bOpen := b.Status == models.TicketStatusOpen
	if aOpen != bOpen {
		return aOpen
	}
	return a.CreatedAt > b.CreatedAt
}
