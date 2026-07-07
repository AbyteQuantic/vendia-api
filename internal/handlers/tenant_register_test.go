package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"vendia-backend/internal/auth"
	"vendia-backend/internal/config"
	"vendia-backend/internal/database"
	"vendia-backend/internal/handlers"
	"vendia-backend/internal/models"
)

const testSecret = "test-jwt-secret-vendia-2024-long-enough-32"

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	// Quick TCP check to avoid 30s retry timeout when DB is not running
	conn, err := net.DialTimeout("tcp", "localhost:5499", 1*time.Second)
	if err != nil {
		t.Skip("Skipping: Docker PostgreSQL not available (run 'make local')")
	}
	conn.Close()

	cfg := &config.Config{
		DatabaseURL: "postgres://vendia:vendia_secret@localhost:5499/vendia?sslmode=disable",
		JWTSecret:   testSecret,
	}
	db, err := database.Connect(cfg)
	if err != nil {
		t.Skip("Skipping: Docker PostgreSQL not available (run 'make local')")
	}
	require.NoError(t, database.Migrate(db), "migration failed")
	return db
}

func setupRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/tenant/register", handlers.TenantRegister(db, testSecret))
	return r
}

func uniquePhone() string {
	return fmt.Sprintf("999%07d", time.Now().UnixNano()%10000000)
}

func cleanupByPhone(t *testing.T, db *gorm.DB, phone string) {
	t.Helper()
	var tenant models.Tenant
	if err := db.Unscoped().Where("phone = ?", phone).First(&tenant).Error; err == nil {
		db.Unscoped().Where("tenant_id = ?", tenant.ID).Delete(&models.Employee{})
		db.Unscoped().Where("tenant_id = ?", tenant.ID).Delete(&models.RefreshToken{})
		db.Unscoped().Where("tenant_id = ?", tenant.ID).Delete(&models.TenantSubscription{})
		db.Unscoped().Delete(&tenant)
	}
}

type ownerPayload struct {
	Name     string `json:"name"`
	Phone    string `json:"phone"`
	Password string `json:"password"`
}

type businessPayload struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	RazonSocial string `json:"razon_social,omitempty"`
	NIT         string `json:"nit,omitempty"`
	Address     string `json:"address,omitempty"`
}

type configPayload struct {
	SaleTypes      []string `json:"sale_types"`
	HasShowcases   bool     `json:"has_showcases"`
	HasTables      bool     `json:"has_tables"`
	OffersServices bool     `json:"offers_services"`
	SellsByWeight  bool     `json:"sells_by_weight"`
}

type employeePayload struct {
	Name     string `json:"name"`
	Phone    string `json:"phone,omitempty"`
	Role     string `json:"role"`
	Password string `json:"password"`
}

type fullRegisterPayload struct {
	Owner       ownerPayload      `json:"owner"`
	Business    businessPayload   `json:"business"`
	Config      configPayload     `json:"config"`
	Employees   []employeePayload `json:"employees,omitempty"`
	AcceptTerms bool              `json:"accept_terms"` // Spec 098
}

func defaultPayload(phone string) fullRegisterPayload {
	return fullRegisterPayload{
		Owner:       ownerPayload{Name: "Test Owner", Phone: phone, Password: "1234"},
		Business:    businessPayload{Name: "Test Store", Type: "tienda_barrio"},
		Config:      configPayload{SaleTypes: []string{"products"}},
		AcceptTerms: true, // Spec 098 — el registro exige aceptar términos
	}
}

func postJSON(router *gin.Engine, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/tenant/register", bytes.NewBuffer(b))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

func TestTenantRegister_Success_WithEmployees(t *testing.T) {
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	payload := defaultPayload(phone)
	payload.Business.RazonSocial = "Test SAS"
	payload.Business.NIT = "900000001-0"
	payload.Business.Address = "Calle 1 #1-1"
	payload.Employees = []employeePayload{
		{Name: "Test Cajero", Phone: uniquePhone(), Role: "cashier", Password: "5678"},
	}

	w := postJSON(setupRouter(db), payload)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	token, ok := resp["token"].(string)
	require.True(t, ok, "response must contain 'token' string")
	assert.NotEmpty(t, token)

	claims, err := auth.ValidateToken(token, testSecret)
	require.NoError(t, err, "JWT must be valid")
	assert.Equal(t, phone, claims.Phone)
	assert.Equal(t, "Test Store", claims.BusinessName)
	assert.NotEmpty(t, claims.TenantID)

	assert.NotNil(t, resp["tenant_id"])
	assert.Equal(t, "Test Owner", resp["owner_name"])

	tenantID := resp["tenant_id"].(string)
	var employees []models.Employee
	db.Where("tenant_id = ?", tenantID).Find(&employees)
	require.Len(t, employees, 1)
	assert.Equal(t, "Test Cajero", employees[0].Name)
	assert.Equal(t, models.RoleCashier, employees[0].Role)
	assert.False(t, employees[0].IsOwner)
}

func TestTenantRegister_Success_OwnerAsCashier(t *testing.T) {
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	payload := defaultPayload(phone)

	w := postJSON(setupRouter(db), payload)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	tenantID := resp["tenant_id"].(string)
	var employees []models.Employee
	db.Where("tenant_id = ?", tenantID).Find(&employees)

	require.Len(t, employees, 1)
	emp := employees[0]
	assert.True(t, emp.IsOwner)
	assert.Equal(t, models.RoleCashier, emp.Role)
	assert.Equal(t, "Test Owner", emp.Name)
}

func TestTenantRegister_DuplicatePhone(t *testing.T) {
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	payload := defaultPayload(phone)
	router := setupRouter(db)

	w1 := postJSON(router, payload)
	require.Equal(t, http.StatusCreated, w1.Code)

	w2 := postJSON(router, payload)
	assert.Equal(t, http.StatusConflict, w2.Code)
}

func TestTenantRegister_InvalidPayload(t *testing.T) {
	db := setupTestDB(t)
	router := setupRouter(db)

	cases := []struct {
		name    string
		payload map[string]any
	}{
		{
			name:    "owner missing",
			payload: map[string]any{"business": map[string]any{"name": "X", "type": "tienda_barrio"}, "config": map[string]any{"sale_types": []string{"products"}}},
		},
		{
			name:    "business missing",
			payload: map[string]any{"owner": map[string]any{"name": "X", "phone": uniquePhone(), "password": "1234"}, "config": map[string]any{"sale_types": []string{"products"}}},
		},
		{
			name:    "config missing",
			payload: map[string]any{"owner": map[string]any{"name": "X", "phone": uniquePhone(), "password": "1234"}, "business": map[string]any{"name": "X", "type": "tienda_barrio"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postJSON(router, tc.payload)
			assert.Equal(t, http.StatusBadRequest, w.Code, "case '%s': body=%s", tc.name, w.Body.String())
		})
	}
}

func TestTenantRegister_JWT_HasCorrectExpiry(t *testing.T) {
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	w := postJSON(setupRouter(db), defaultPayload(phone))
	require.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	token := resp["token"].(string)
	claims, err := auth.ValidateToken(token, testSecret)
	require.NoError(t, err)

	// El token de acceso dura auth.AccessTokenDuration (7 días — fricción
	// cero para tiendas, ver auth/jwt.go). Referenciamos la constante para
	// que la prueba no se desactualice si el valor cambia, dejando ±2 min de
	// tolerancia por la latencia del request en CI.
	expiresAt := claims.ExpiresAt.Time
	expectedMin := time.Now().Add(auth.AccessTokenDuration - 2*time.Minute)
	expectedMax := time.Now().Add(auth.AccessTokenDuration + 2*time.Minute)

	assert.True(t, expiresAt.After(expectedMin),
		"el token debe expirar ~%s en el futuro", auth.AccessTokenDuration)
	assert.True(t, expiresAt.Before(expectedMax),
		"el token no debe expirar más allá de ~%s", auth.AccessTokenDuration)

	refreshToken, ok := resp["refresh_token"].(string)
	assert.True(t, ok, "response must contain refresh_token")
	assert.NotEmpty(t, refreshToken)
}

// TestTenantRegister_CreatesTrialSubscription verifies AC-01: registering
// a tenant creates its TenantSubscription row in TRIAL state with a
// trial_ends_at models.TrialDays out — inside the registration transaction,
// not via a DB trigger Render never runs.
func TestTenantRegister_CreatesTrialSubscription(t *testing.T) {
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	before := time.Now()
	w := postJSON(setupRouter(db), defaultPayload(phone))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	tenantID := resp["tenant_id"].(string)

	var sub models.TenantSubscription
	require.NoError(t,
		db.Where("tenant_id = ?", tenantID).First(&sub).Error,
		"el registro DEBE crear la TenantSubscription (AC-01)")

	assert.Equal(t, models.SubscriptionStatusTrial, sub.Status)
	assert.Equal(t, models.SubscriptionPlanFree, sub.Plan,
		"el trial arranca en el plan base")
	require.NotNil(t, sub.TrialEndsAt, "trial_ends_at no puede ser nil")

	// models.TrialDays (7) ±1 hora de tolerancia. Referenciamos la constante
	// para que la prueba siga al valor real del trial (reverse trial, F-SaaS).
	trial := time.Duration(models.TrialDays) * 24 * time.Hour
	expectedMin := before.Add(trial - 1*time.Hour)
	expectedMax := before.Add(trial + 1*time.Hour)
	assert.True(t, sub.TrialEndsAt.After(expectedMin),
		"trial_ends_at debe estar ~%d días en el futuro", models.TrialDays)
	assert.True(t, sub.TrialEndsAt.Before(expectedMax),
		"trial_ends_at no debe pasar de ~%d días", models.TrialDays)
}

// ── T-04: Spec F023 — capability toggles in registration ──────────────────

// TestTenantRegister_CapabilityToggles verifies that config.offers_services,
// config.sells_by_weight, and config.has_tables are mapped to the correct
// feature_flags on the created tenant (AC-04, FR-04, FR-07).
// Requires Docker PostgreSQL — skips gracefully without it.
func TestTenantRegister_CapabilityToggles_OffersServices(t *testing.T) {
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	payload := defaultPayload(phone) // tienda_barrio base
	payload.Config.OffersServices = true

	w := postJSON(setupRouter(db), payload)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	tenantID := resp["tenant_id"].(string)

	var tenant models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&tenant).Error)

	assert.True(t, tenant.FeatureFlags.EnableServices,
		"offers_services toggle debe activar enable_services")
	assert.True(t, tenant.FeatureFlags.EnableCustomBilling,
		"offers_services toggle debe activar enable_custom_billing")
	assert.False(t, tenant.FeatureFlags.EnableKDS,
		"tienda_barrio no debe tener KDS aunque tenga services toggle")
	assert.False(t, tenant.FeatureFlags.EnableTips,
		"tienda_barrio no debe tener tips aunque tenga services toggle")
}

func TestTenantRegister_CapabilityToggles_SellsByWeight(t *testing.T) {
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	payload := defaultPayload(phone) // tienda_barrio base
	payload.Config.SellsByWeight = true

	w := postJSON(setupRouter(db), payload)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	tenantID := resp["tenant_id"].(string)

	var tenant models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&tenant).Error)

	assert.True(t, tenant.FeatureFlags.EnableFractionalUnits,
		"sells_by_weight toggle debe activar enable_fractional_units")
	assert.False(t, tenant.FeatureFlags.EnableTables,
		"sells_by_weight no debe activar mesas")
}

func TestTenantRegister_CapabilityToggles_HasTables(t *testing.T) {
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	payload := defaultPayload(phone) // tienda_barrio base
	payload.Config.HasTables = true

	w := postJSON(setupRouter(db), payload)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	tenantID := resp["tenant_id"].(string)

	var tenant models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&tenant).Error)

	assert.True(t, tenant.FeatureFlags.EnableTables,
		"has_tables toggle debe activar enable_tables")
	assert.False(t, tenant.FeatureFlags.EnableKDS,
		"has_tables en tienda_barrio NO debe activar KDS")
	assert.False(t, tenant.FeatureFlags.EnableTips,
		"has_tables en tienda_barrio NO debe activar tips")
}

func TestTenantRegister_CapabilityToggles_NoToggles_Legacy(t *testing.T) {
	// AC-07: a tenant with no toggles keeps the exact same feature_flags
	// as before Spec F023.
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	payload := defaultPayload(phone) // tienda_barrio, no toggles

	w := postJSON(setupRouter(db), payload)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	tenantID := resp["tenant_id"].(string)

	var tenant models.Tenant
	require.NoError(t, db.Where("id = ?", tenantID).First(&tenant).Error)

	// All flags must be false for tienda_barrio with no toggles (retrocompat)
	assert.Equal(t, models.FeatureFlags{}, tenant.FeatureFlags,
		"tienda_barrio sin toggles debe tener todos los flags en false (AC-07)")
}

// ── Spec F037 — defaults mínimos (revierte la auto-activación de F036) ──────

// loadTenantByPhone fetches the freshly-registered tenant so a test can
// assert on its enable_* columns and onboarding flag.
func loadTenantByPhone(t *testing.T, db *gorm.DB, phone string) models.Tenant {
	t.Helper()
	var tenant models.Tenant
	require.NoError(t, db.Where("phone = ?", phone).First(&tenant).Error)
	return tenant
}

// assertNoOptionalCapabilities is the F037 contract pinned to every
// business type: no matter what type the tenant picks at registration,
// zero optional capability flags get auto-activated. The tendero
// discovers them later through the Dashboard reel (Spec F037 §4.1).
func assertNoOptionalCapabilities(t *testing.T, tenant models.Tenant, typeLabel string) {
	t.Helper()
	assert.False(t, tenant.FeatureFlags.EnableTables,
		"%s NO debe nacer con mesas (F037)", typeLabel)
	assert.False(t, tenant.FeatureFlags.EnableServices,
		"%s NO debe nacer con servicios (F037)", typeLabel)
	assert.False(t, tenant.FeatureFlags.EnableKDS,
		"%s NO debe nacer con KDS (F037)", typeLabel)
	assert.False(t, tenant.FeatureFlags.EnableTips,
		"%s NO debe nacer con tips (F037)", typeLabel)
	assert.False(t, tenant.FeatureFlags.EnableFractionalUnits,
		"%s NO debe nacer con fractional units (F037)", typeLabel)
	assert.False(t, tenant.EnablePriceTiers,
		"%s NO debe nacer con precios multi-tier (F037)", typeLabel)
	assert.False(t, tenant.EnableCustomerManagement,
		"%s NO debe nacer con gestión de clientes (F037)", typeLabel)
	assert.False(t, tenant.EnableQuotes,
		"%s NO debe nacer con cotizaciones (F037)", typeLabel)
	assert.False(t, tenant.EnablePromotions,
		"%s NO debe nacer con promociones (F037)", typeLabel)
	assert.False(t, tenant.EnableMarketingHub,
		"%s NO debe nacer con marketing hub (F037)", typeLabel)
	assert.False(t, tenant.EnableRecipes,
		"%s NO debe nacer con recetas (F037)", typeLabel)
	assert.False(t, tenant.EnableSupplies,
		"%s NO debe nacer con insumos (F037)", typeLabel)
	assert.False(t, tenant.EnableFurnitureJobs,
		"%s NO debe nacer con trabajos de muebles (F037)", typeLabel)
	assert.False(t, tenant.EnablePurchaseOrders,
		"%s NO debe nacer con órdenes de compra (F037)", typeLabel)
	assert.False(t, tenant.OnboardingCompleted,
		"%s nace con onboarding_completed=false (F036/F037)", typeLabel)
}

// TestTenantRegister_NoCapabilitiesByType is the F037 AC-01 / AC-09
// contract: NO business type pre-activates ANY optional capability at
// registration. Replaces the F036 per-type pre-activation tests.
func TestTenantRegister_NoCapabilitiesByType(t *testing.T) {
	cases := []struct {
		label string
		typ   string
	}{
		{"tienda_barrio", "tienda_barrio"},
		{"restaurante", "restaurante"},
		{"manufactura", "manufactura"},
		{"deposito_construccion", "deposito_construccion"},
		{"bar", "bar"},
		{"comidas_rapidas", "comidas_rapidas"},
		{"reparacion_muebles", "reparacion_muebles"},
		{"emprendimiento_general", "emprendimiento_general"},
		{"minimercado", "minimercado"},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			db := setupTestDB(t)
			phone := uniquePhone()
			t.Cleanup(func() { cleanupByPhone(t, db, phone) })

			payload := defaultPayload(phone)
			payload.Business.Type = tc.typ

			w := postJSON(setupRouter(db), payload)
			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

			tenant := loadTenantByPhone(t, db, phone)
			assertNoOptionalCapabilities(t, tenant, tc.label)
		})
	}
}

// Spec 075 — registrarse como proveedor B2B debe derivar EnableSupplierMode
// (panel de proveedor) y persistir el tipo. Cierra el flujo de ONBOARDING de
// proveedores (antes solo existían por seed). Corre en CI con Postgres; se
// salta sin DB local (TCP pre-check de setupTestDB).
func TestRegister_Proveedor_DerivaSupplierMode(t *testing.T) {
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	payload := defaultPayload(phone)
	payload.Business.Type = "proveedor_mayorista"
	w := postJSON(setupRouter(db), payload)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var tenant models.Tenant
	require.NoError(t, db.Where("phone = ?", phone).First(&tenant).Error)
	assert.Contains(t, tenant.BusinessTypes, "proveedor_mayorista",
		"el tipo de proveedor debe persistir en el tenant")
	assert.True(t, tenant.FeatureFlags.EnableSupplierMode,
		"registrarse como proveedor debe activar el Panel de proveedor")
}

// El tipo agrícola también activa el modo proveedor.
func TestRegister_ProveedorAgricola_DerivaSupplierMode(t *testing.T) {
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	payload := defaultPayload(phone)
	payload.Business.Type = "proveedor_agricola"
	w := postJSON(setupRouter(db), payload)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var tenant models.Tenant
	require.NoError(t, db.Where("phone = ?", phone).First(&tenant).Error)
	assert.True(t, tenant.FeatureFlags.EnableSupplierMode)
}

// Una tienda normal NO debe quedar en modo proveedor (regresión del AC-01).
func TestRegister_TiendaNormal_SinSupplierMode(t *testing.T) {
	db := setupTestDB(t)
	phone := uniquePhone()
	t.Cleanup(func() { cleanupByPhone(t, db, phone) })

	w := postJSON(setupRouter(db), defaultPayload(phone)) // tienda_barrio
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var tenant models.Tenant
	require.NoError(t, db.Where("phone = ?", phone).First(&tenant).Error)
	assert.False(t, tenant.FeatureFlags.EnableSupplierMode)
}
