// Spec: specs/084-peluqueria-salon/spec.md (Fase 2 — citas/turnos)
//
// Citas/turnos de peluquería/barbería: agenda del salón (JWT) + reserva pública
// desde el catálogo (sin auth). La disponibilidad se calcula sobre una ventana
// de atención por defecto (08:00–19:00 hora Colombia) excluyendo las citas ya
// tomadas del profesional. Aditivo; reusa la atribución por profesional (Fase 1).
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

// Colombia no tiene DST: offset fijo -05:00.
var bogotaLoc = time.FixedZone("America/Bogota", -5*60*60)

const (
	bookingOpenHour  = 8  // 08:00
	bookingCloseHour = 19 // 19:00
	defaultSlotMin   = 30 // duración por defecto si el servicio no la define
)

// parseDateParam acepta RFC3339 o YYYY-MM-DD (hora Colombia); nil si vacío/inválido.
func parseDateParam(s string) *time.Time {
	if s == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t
	}
	if t, err := time.ParseInLocation("2006-01-02", s, bogotaLoc); err == nil {
		return &t
	}
	return nil
}

// ── Agenda del salón (JWT) ──────────────────────────────────────────────────

// ListAppointments — GET /api/v1/appointments?from=&until=&employee_uuid=&status=
func ListAppointments(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		// La agenda incluye el FUTURO: default [ayer, +90 días]. Acepta override
		// con from/until (YYYY-MM-DD o RFC3339).
		now := time.Now()
		from := now.AddDate(0, 0, -1)
		until := now.AddDate(0, 0, 90)
		if v := parseDateParam(c.Query("from")); v != nil {
			from = *v
		}
		if v := parseDateParam(c.Query("until")); v != nil {
			until = v.Add(24 * time.Hour)
		}
		q := db.Where("tenant_id = ? AND starts_at >= ? AND starts_at < ?",
			tenantID, from, until)
		if e := strings.TrimSpace(c.Query("employee_uuid")); e != "" {
			q = q.Where("employee_uuid = ?", e)
		}
		if s := strings.TrimSpace(c.Query("status")); s != "" {
			q = q.Where("status = ?", s)
		}
		var rows []models.Appointment
		q.Order("starts_at ASC").Limit(500).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"data": rows})
	}
}

// CreateAppointment — POST /api/v1/appointments (creada por el salón).
func CreateAppointment(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var a models.Appointment
		if err := c.ShouldBindJSON(&a); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if a.StartsAt.IsZero() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "indique la fecha y hora"})
			return
		}
		a.TenantID = tenantID
		if a.BranchID == nil {
			a.BranchID = middleware.GetBranchIDPtr(c)
		}
		if a.EndsAt.IsZero() {
			a.EndsAt = a.StartsAt.Add(defaultSlotMin * time.Minute)
		}
		if a.Status == "" {
			a.Status = models.AppointmentConfirmed
		}
		a.Source = models.AppointmentSourcePOS
		if err := db.Create(&a).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo crear la cita"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": a})
	}
}

// UpdateAppointment — PATCH /api/v1/appointments/:id (estado / reprogramar /
// asignar profesional).
func UpdateAppointment(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Status       *string    `json:"status"`
		EmployeeUUID *string    `json:"employee_uuid"`
		EmployeeName *string    `json:"employee_name"`
		StartsAt     *time.Time `json:"starts_at"`
		EndsAt       *time.Time `json:"ends_at"`
		Notes        *string    `json:"notes"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		id := c.Param("id")
		var a models.Appointment
		if err := db.Where("id = ? AND tenant_id = ?", id, tenantID).First(&a).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "cita no encontrada"})
			return
		}
		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		updates := map[string]any{}
		if req.Status != nil {
			if _, ok := models.ValidAppointmentStatuses[*req.Status]; !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "estado no válido"})
				return
			}
			updates["status"] = *req.Status
		}
		if req.EmployeeUUID != nil {
			updates["employee_uuid"] = middleware.UUIDPtr(*req.EmployeeUUID)
		}
		if req.EmployeeName != nil {
			updates["employee_name"] = *req.EmployeeName
		}
		if req.StartsAt != nil {
			updates["starts_at"] = *req.StartsAt
		}
		if req.EndsAt != nil {
			updates["ends_at"] = *req.EndsAt
		}
		if req.Notes != nil {
			updates["notes"] = *req.Notes
		}
		if len(updates) == 0 {
			c.JSON(http.StatusOK, gin.H{"data": a})
			return
		}
		if err := db.Model(&a).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo actualizar"})
			return
		}
		db.Where("id = ?", id).First(&a)
		c.JSON(http.StatusOK, gin.H{"data": a})
	}
}

// ── Reserva pública (sin auth) ──────────────────────────────────────────────

func publicTenantBySlug(db *gorm.DB, slug string) (*models.Tenant, error) {
	var t models.Tenant
	if err := db.Where("store_slug = ?", slug).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// PublicBookableServices — GET /api/v1/store/:slug/booking/services
// Servicios reservables (is_service, no ocultos del catálogo).
func PublicBookableServices(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		t, err := publicTenantBySlug(db, c.Param("slug"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}
		var products []models.Product
		db.Where("tenant_id = ? AND is_service = ?", t.ID, true).
			Order("name ASC").Find(&products)
		type Svc struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			Price       float64 `json:"price"`
			DurationMin int     `json:"duration_min"`
			PhotoURL    string  `json:"photo_url"`
		}
		out := make([]Svc, 0, len(products))
		for _, p := range products {
			d := defaultSlotMin
			if p.DurationMin != nil && *p.DurationMin > 0 {
				d = *p.DurationMin
			}
			out = append(out, Svc{ID: p.ID, Name: p.Name, Price: p.Price, DurationMin: d, PhotoURL: p.PhotoURL})
		}
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}

// PublicBookingStaff — GET /api/v1/store/:slug/booking/staff
// Profesionales activos disponibles para reservar.
func PublicBookingStaff(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		t, err := publicTenantBySlug(db, c.Param("slug"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}
		var emps []models.Employee
		db.Where("tenant_id = ? AND is_active = ?", t.ID, true).
			Order("name ASC").Find(&emps)
		type Pro struct {
			UUID     string `json:"uuid"`
			Name     string `json:"name"`
			PhotoURL string `json:"photo_url"`
		}
		out := make([]Pro, 0, len(emps))
		for _, e := range emps {
			out = append(out, Pro{UUID: e.ID, Name: e.Name, PhotoURL: e.PhotoURL})
		}
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}

// PublicAvailability — GET /api/v1/store/:slug/booking/availability?employee_uuid=&date=YYYY-MM-DD&duration_min=
// Franjas libres del profesional ese día (ventana 08:00–19:00, hora Colombia).
func PublicAvailability(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		t, err := publicTenantBySlug(db, c.Param("slug"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}
		emp := strings.TrimSpace(c.Query("employee_uuid"))
		if emp == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "indique el profesional"})
			return
		}
		day, err := time.ParseInLocation("2006-01-02", c.Query("date"), bogotaLoc)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "fecha no válida"})
			return
		}
		dur := defaultSlotMin
		if d := c.Query("duration_min"); d != "" {
			if v, e := time.ParseDuration(d + "m"); e == nil && v > 0 {
				dur = int(v.Minutes())
			}
		}

		dayStart := time.Date(day.Year(), day.Month(), day.Day(), bookingOpenHour, 0, 0, 0, bogotaLoc)
		dayEnd := time.Date(day.Year(), day.Month(), day.Day(), bookingCloseHour, 0, 0, 0, bogotaLoc)

		// Citas ya tomadas del profesional ese día (no canceladas).
		var taken []models.Appointment
		db.Where("tenant_id = ? AND employee_uuid = ? AND status != ? AND starts_at >= ? AND starts_at < ?",
			t.ID, emp, models.AppointmentCancelled, dayStart, dayEnd).Find(&taken)

		now := time.Now()
		step := time.Duration(dur) * time.Minute
		var slots []string
		for s := dayStart; !s.Add(step).After(dayEnd); s = s.Add(step) {
			if s.Before(now) {
				continue // no ofrecer franjas pasadas
			}
			end := s.Add(step)
			overlap := false
			for _, a := range taken {
				if s.Before(a.EndsAt) && end.After(a.StartsAt) {
					overlap = true
					break
				}
			}
			if !overlap {
				slots = append(slots, s.Format(time.RFC3339))
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"slots": slots}})
	}
}

// PublicCreateBooking — POST /api/v1/store/:slug/booking
// Reserva una cita desde el catálogo público.
func PublicCreateBooking(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		ProductID     string    `json:"product_id"`
		EmployeeUUID  string    `json:"employee_uuid"`
		CustomerName  string    `json:"customer_name" binding:"required"`
		CustomerPhone string    `json:"customer_phone" binding:"required"`
		StartsAt      time.Time `json:"starts_at" binding:"required"`
		Notes         string    `json:"notes"`
	}
	return func(c *gin.Context) {
		t, err := publicTenantBySlug(db, c.Param("slug"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}
		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "faltan datos de la reserva"})
			return
		}
		if req.StartsAt.Before(time.Now()) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "elija un horario futuro"})
			return
		}

		appt := models.Appointment{
			TenantID:      t.ID,
			CustomerName:  strings.TrimSpace(req.CustomerName),
			CustomerPhone: strings.TrimSpace(req.CustomerPhone),
			StartsAt:      req.StartsAt,
			Status:        models.AppointmentPending,
			Source:        models.AppointmentSourcePublic,
			Notes:         strings.TrimSpace(req.Notes),
		}
		dur := defaultSlotMin
		if req.ProductID != "" {
			var p models.Product
			if err := db.Where("id = ? AND tenant_id = ?", req.ProductID, t.ID).First(&p).Error; err == nil {
				appt.ProductID = &p.ID
				appt.ServiceName = p.Name
				appt.Price = p.Price
				if p.DurationMin != nil && *p.DurationMin > 0 {
					dur = *p.DurationMin
				}
			}
		}
		if req.EmployeeUUID != "" {
			var e models.Employee
			if err := db.Where("id = ? AND tenant_id = ?", req.EmployeeUUID, t.ID).First(&e).Error; err == nil {
				appt.EmployeeUUID = &e.ID
				appt.EmployeeName = e.Name
			}
		}
		appt.EndsAt = appt.StartsAt.Add(time.Duration(dur) * time.Minute)

		// Anti doble-reserva del mismo profesional en la franja (best-effort).
		if appt.EmployeeUUID != nil {
			var clash int64
			db.Model(&models.Appointment{}).
				Where("tenant_id = ? AND employee_uuid = ? AND status != ? AND starts_at < ? AND ? < ends_at",
					t.ID, *appt.EmployeeUUID, models.AppointmentCancelled, appt.EndsAt, appt.StartsAt).
				Count(&clash)
			if clash > 0 {
				c.JSON(http.StatusConflict, gin.H{"error": "ese horario ya fue reservado, elija otro"})
				return
			}
		}

		if err := db.Create(&appt).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo reservar"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": gin.H{
			"id":         appt.ID,
			"starts_at":  appt.StartsAt.Format(time.RFC3339),
			"service":    appt.ServiceName,
			"profesional": appt.EmployeeName,
			"estado":     appt.Status,
		}})
	}
}
