// Spec: specs/051-login-emite-capacidades/spec.md
//
// Bug real (reportado por el dueño, "me ha pasado muchas veces"): una capacidad
// ACTIVA (p.ej. Recetas/menú) seguía apareciendo en "Descubre más opciones —
// Toca para activar" en vez de subir al carrusel activo. Causa raíz: las
// capacidades nuevas (enable_recipes, enable_marketing_hub, enable_quotes, …)
// viven como COLUMNAS top-level del tenant, NO dentro del JSONB feature_flags.
// La respuesta de /login solo serializaba el JSONB (7 flags viejos) y omitía
// esas columnas, así que tras cada login la app las veía en false y el
// dashboard las re-clasificaba como "sin activar". La app YA sabe mergear esas
// llaves top-level (_saveFeatureFlags); solo faltaba que el backend las mandara.
package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupCapFlagsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
			phone TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL DEFAULT '',
			password_hash TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY, deleted_at DATETIME,
			business_name TEXT DEFAULT '', phone TEXT DEFAULT '',
			password_hash TEXT DEFAULT '', store_slug TEXT DEFAULT '',
			created_at DATETIME, updated_at DATETIME,
			feature_flags TEXT DEFAULT '{}', business_types TEXT DEFAULT '[]',
			credit_label_mode TEXT DEFAULT 'fiar',
			-- columnas de capacidad top-level (Spec 029-037)
			enable_recipes INTEGER DEFAULT 0,
			enable_marketing_hub INTEGER DEFAULT 0,
			enable_quotes INTEGER DEFAULT 0,
			enable_promotions INTEGER DEFAULT 0,
			enable_customer_management INTEGER DEFAULT 0,
			enable_supplies INTEGER DEFAULT 0,
			enable_furniture_jobs INTEGER DEFAULT 0,
			enable_purchase_orders INTEGER DEFAULT 0,
			enable_price_tiers INTEGER DEFAULT 0,
			terms_accepted_version TEXT DEFAULT '' -- Spec 098
		);
		CREATE TABLE branches (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL,
			name TEXT NOT NULL, address TEXT DEFAULT '', is_active INTEGER DEFAULT 1
		);
		CREATE TABLE user_workspaces (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, user_id TEXT NOT NULL, tenant_id TEXT NOT NULL,
			branch_id TEXT, role TEXT NOT NULL DEFAULT 'owner', is_default INTEGER DEFAULT 0
		);
		CREATE TABLE employees (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, tenant_id TEXT NOT NULL, branch_id TEXT,
			name TEXT NOT NULL, phone TEXT DEFAULT '', pin TEXT DEFAULT '',
			role TEXT NOT NULL DEFAULT 'cashier', password_hash TEXT NOT NULL DEFAULT '',
			is_owner INTEGER DEFAULT 0, is_active INTEGER DEFAULT 1
		);
		CREATE TABLE refresh_tokens (
			id TEXT PRIMARY KEY, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME, user_id TEXT, tenant_id TEXT,
			token TEXT NOT NULL, expires_at DATETIME NOT NULL, revoked INTEGER DEFAULT 0
		);
	`).Error)
	return db
}

// El login de un dueño con UN solo workspace toma el camino
// createWorkspaceTokenPair. La respuesta DEBE incluir las capacidades top-level
// activas para que el dashboard no las degrade a "Descubre más opciones".
func TestLogin_EmitsTopLevelCapabilityFlags(t *testing.T) {
	db := setupCapFlagsDB(t)
	phone := "3001234567"
	pwd := "1234"

	userID := uuid.NewString()
	tenantID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO users (id, phone, name, password_hash, created_at) VALUES (?, ?, 'Dueño', ?, datetime('now'))`,
		userID, phone, bcryptHash(t, pwd),
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO tenants (id, business_name, phone, feature_flags, business_types,
			enable_recipes, enable_marketing_hub, enable_quotes, enable_customer_management,
			created_at, updated_at)
		VALUES (?, 'Don Brayan', ?, '{"enable_tables":true}', '["tienda_barrio"]',
			1, 1, 1, 1, datetime('now'), datetime('now'))`,
		tenantID, phone,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO user_workspaces (id, user_id, tenant_id, role, is_default, created_at)
		 VALUES (?, ?, ?, 'owner', 1, datetime('now'))`,
		uuid.NewString(), userID, tenantID,
	).Error)

	r := mountLogin(db)
	body, _ := json.Marshal(map[string]any{"phone": phone, "password": pwd})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	gin.SetMode(gin.TestMode)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Las columnas top-level activas DEBEN viajar en la respuesta.
	assert.Equal(t, true, resp["enable_recipes"], "recipes activo debe viajar al cliente")
	assert.Equal(t, true, resp["enable_marketing_hub"])
	assert.Equal(t, true, resp["enable_quotes"])
	assert.Equal(t, true, resp["enable_customer_management"])
	// Las inactivas viajan en false (no ausentes), para apagar correctamente.
	assert.Equal(t, false, resp["enable_promotions"])
	assert.Equal(t, false, resp["enable_purchase_orders"])
}
