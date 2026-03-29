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
	SaleTypes    []string `json:"sale_types"`
	HasShowcases bool     `json:"has_showcases"`
	HasTables    bool     `json:"has_tables"`
}

type employeePayload struct {
	Name     string `json:"name"`
	Phone    string `json:"phone,omitempty"`
	Role     string `json:"role"`
	Password string `json:"password"`
}

type fullRegisterPayload struct {
	Owner     ownerPayload      `json:"owner"`
	Business  businessPayload   `json:"business"`
	Config    configPayload     `json:"config"`
	Employees []employeePayload `json:"employees,omitempty"`
}

func defaultPayload(phone string) fullRegisterPayload {
	return fullRegisterPayload{
		Owner:    ownerPayload{Name: "Test Owner", Phone: phone, Password: "1234"},
		Business: businessPayload{Name: "Test Store", Type: "tienda_barrio"},
		Config:   configPayload{SaleTypes: []string{"products"}},
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

	expiresAt := claims.ExpiresAt.Time
	expectedMin := time.Now().Add(14 * time.Minute)
	expectedMax := time.Now().Add(16 * time.Minute)

	assert.True(t, expiresAt.After(expectedMin))
	assert.True(t, expiresAt.Before(expectedMax))

	refreshToken, ok := resp["refresh_token"].(string)
	assert.True(t, ok, "response must contain refresh_token")
	assert.NotEmpty(t, refreshToken)
}
