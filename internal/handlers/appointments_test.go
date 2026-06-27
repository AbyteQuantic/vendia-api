// Spec: specs/084-peluqueria-salon/spec.md (Fase 2 — citas/turnos)
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func setupApptDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(
		`CREATE TABLE tenants (id TEXT PRIMARY KEY, deleted_at DATETIME,
			store_slug TEXT DEFAULT '', created_at DATETIME)`).Error)
	require.NoError(t, db.AutoMigrate(
		&models.Employee{}, &models.Product{}, &models.Appointment{},
		&models.EmployeePayConfig{}, &models.Sale{}, &models.SaleItem{}))
	return db
}

func mountAppt(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Públicas.
	r.GET("/store/:slug/booking/availability", handlers.PublicAvailability(db))
	r.POST("/store/:slug/booking", handlers.PublicCreateBooking(db))
	r.GET("/store/:slug/booking/services", handlers.PublicBookableServices(db))
	// JWT (inyecta tenant).
	auth := r.Group("/", func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	auth.GET("/appointments", handlers.ListAppointments(db))
	auth.POST("/appointments/:id/convert", handlers.ConvertAppointmentToSale(db))
	return r
}

// Reserva pública: crea cita, la disponibilidad la excluye, y un segundo intento
// en la misma franja choca (409).
func TestPublicBooking_AvailabilityAndDoubleBook(t *testing.T) {
	db := setupApptDB(t)
	tenantID := uuid.NewString()
	empID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, store_slug, created_at) VALUES (?, 'salon', ?)`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Create(&models.Employee{
		BaseModel: models.BaseModel{ID: empID}, TenantID: tenantID,
		Name: "Ana", IsActive: true,
	}).Error)
	dur := 30
	prod := models.Product{
		BaseModel: models.BaseModel{ID: uuid.NewString()}, TenantID: tenantID,
		Name: "Corte", Price: 10000, IsService: true, DurationMin: &dur,
	}
	require.NoError(t, db.Create(&prod).Error)

	r := mountAppt(db, tenantID)

	// Franja futura concreta: mañana 10:00 hora Colombia.
	loc := time.FixedZone("America/Bogota", -5*60*60)
	now := time.Now().In(loc)
	day := now.AddDate(0, 0, 1)
	slot := time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, loc)
	dateStr := slot.Format("2006-01-02")

	// Disponibilidad inicial incluye las 10:00.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/store/salon/booking/availability?employee_uuid="+empID+"&date="+dateStr+"&duration_min=30", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), slot.Format(time.RFC3339))

	// Reservar las 10:00.
	book := doJSON(t, r, http.MethodPost, "/store/salon/booking", map[string]any{
		"product_id":     prod.ID,
		"employee_uuid":  empID,
		"customer_name":  "Cliente",
		"customer_phone": "3001234567",
		"starts_at":      slot.Format(time.RFC3339),
	})
	require.Equal(t, http.StatusCreated, book.Code, book.Body.String())

	// Ahora la disponibilidad ya NO incluye las 10:00.
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet,
		"/store/salon/booking/availability?employee_uuid="+empID+"&date="+dateStr+"&duration_min=30", nil))
	assert.NotContains(t, w2.Body.String(), slot.Format(time.RFC3339))

	// Segundo intento en la misma franja → 409.
	clash := doJSON(t, r, http.MethodPost, "/store/salon/booking", map[string]any{
		"product_id":     prod.ID,
		"employee_uuid":  empID,
		"customer_name":  "Otro",
		"customer_phone": "3009999999",
		"starts_at":      slot.Format(time.RFC3339),
	})
	assert.Equal(t, http.StatusConflict, clash.Code, clash.Body.String())

	// La agenda del salón lista la cita.
	ag := httptest.NewRecorder()
	r.ServeHTTP(ag, httptest.NewRequest(http.MethodGet, "/appointments", nil))
	require.Equal(t, http.StatusOK, ag.Code)
	assert.Contains(t, ag.Body.String(), "Ana")
	assert.Contains(t, ag.Body.String(), "Corte")
}

// Convertir cita → venta con comisión congelada; idempotente.
func TestConvertAppointmentToSale(t *testing.T) {
	db := setupApptDB(t)
	tenantID := uuid.NewString()
	empID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, store_slug, created_at) VALUES (?, 'salon', ?)`,
		tenantID, time.Now()).Error)
	pct := 40.0
	require.NoError(t, db.Create(&models.EmployeePayConfig{
		BaseModel: models.BaseModel{ID: uuid.NewString()}, TenantID: tenantID, EmployeeUUID: empID,
		PayModel: models.PayModelCommission, CommissionPct: &pct, TipRate: 1,
		EffectiveFrom: time.Now().Add(-time.Hour), IsActive: true,
	}).Error)
	apptID := uuid.NewString()
	require.NoError(t, db.Create(&models.Appointment{
		BaseModel: models.BaseModel{ID: apptID}, TenantID: tenantID,
		EmployeeUUID: &empID, EmployeeName: "Ana", ServiceName: "Corte", Price: 20000,
		StartsAt: time.Now(), EndsAt: time.Now().Add(30 * time.Minute),
		Status: models.AppointmentConfirmed,
	}).Error)

	r := mountAppt(db, tenantID)
	w := doJSON(t, r, http.MethodPost, "/appointments/"+apptID+"/convert",
		map[string]any{"payment_method": "cash"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	// La venta tiene la comisión congelada (20000 * 40% = 8000).
	var item models.SaleItem
	require.NoError(t, db.Where("is_service = ?", true).First(&item).Error)
	assert.Equal(t, 8000.0, item.CommissionAmount)
	assert.Equal(t, empID, *item.EmployeeUUID)

	// La cita quedó atendida + con sale_id.
	var appt models.Appointment
	require.NoError(t, db.First(&appt, "id = ?", apptID).Error)
	assert.Equal(t, models.AppointmentAttended, appt.Status)
	require.NotNil(t, appt.SaleID)

	// Idempotente: segundo convert devuelve la misma venta (200, no duplica).
	w2 := doJSON(t, r, http.MethodPost, "/appointments/"+apptID+"/convert", map[string]any{})
	assert.Equal(t, http.StatusOK, w2.Code)
	var count int64
	db.Model(&models.SaleItem{}).Count(&count)
	assert.Equal(t, int64(1), count, "no debe duplicar la venta")
}

// Reserva en el pasado → 400.
func TestPublicBooking_RejectsPast(t *testing.T) {
	db := setupApptDB(t)
	tenantID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, store_slug, created_at) VALUES (?, 'salon', ?)`,
		tenantID, time.Now()).Error)
	r := mountAppt(db, tenantID)
	past := time.Now().Add(-2 * time.Hour)
	w := doJSON(t, r, http.MethodPost, "/store/salon/booking", map[string]any{
		"customer_name":  "Cliente",
		"customer_phone": "3001234567",
		"starts_at":      past.Format(time.RFC3339),
	})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// Servicios reservables expone duración con default cuando es null.
func TestPublicBookableServices(t *testing.T) {
	db := setupApptDB(t)
	tenantID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, store_slug, created_at) VALUES (?, 'salon', ?)`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: uuid.NewString()}, TenantID: tenantID,
		Name: "Barba", Price: 8000, IsService: true, // sin duración → default
	}).Error)
	r := mountAppt(db, tenantID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/store/salon/booking/services", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Barba")
	assert.Contains(t, w.Body.String(), `"duration_min":30`)
}
