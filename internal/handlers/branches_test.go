package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupBranchesDB opens an in-memory sqlite DB with the minimal
// schema the branches handlers touch. The Tenant model's
// `default:gen_random_uuid()` clause still breaks AutoMigrate here
// (same reason as the admin tenants / ecosystem tests), so we
// hand-craft a narrow tenants table too.
func setupBranchesDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
		CREATE TABLE tenants (
			id TEXT PRIMARY KEY, deleted_at DATETIME,
			business_name TEXT NOT NULL DEFAULT '', phone TEXT DEFAULT '',
			created_at DATETIME
		);
	`).Error)
	require.NoError(t, db.AutoMigrate(&models.Branch{}))
	return db
}

func mountBranches(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if tenantID != "" {
			c.Set(middleware.TenantIDKey, tenantID)
		}
		c.Next()
	})
	r.GET("/store/branches", handlers.ListBranches(db))
	r.POST("/store/branches", handlers.CreateBranch(db))
	r.PATCH("/store/branches/:id", handlers.UpdateBranch(db))
	r.DELETE("/store/branches/:id", handlers.DeleteBranch(db))
	return r
}

func seedTenantAndBranch(t *testing.T, db *gorm.DB, tenantID, branchID, name string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO tenants (id, business_name, created_at)
		VALUES (?, 'Test', ?) ON CONFLICT(id) DO NOTHING`,
		tenantID, time.Now()).Error)
	require.NoError(t, db.Create(&models.Branch{
		BaseModel: models.BaseModel{ID: branchID},
		TenantID:  tenantID, Name: name, IsActive: true,
	}).Error)
}

func doJSON(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestListBranches_ReturnsOnlyTenantOwnedActive(t *testing.T) {
	db := setupBranchesDB(t)
	seedTenantAndBranch(t, db, "tenant-a", "br-a1", "Sede Norte")
	seedTenantAndBranch(t, db, "tenant-a", "br-a2", "Sede Sur")
	seedTenantAndBranch(t, db, "tenant-b", "br-b1", "Sede de Otro Tenant")

	// Archived branch — must NOT appear in the list. Insert via raw
	// SQL because GORM's `default:true` tag on IsActive would
	// silently flip a Go `false` back to true and undermine the
	// assertion (same footgun as the downgraded admin row in the
	// admin-login tests).
	require.NoError(t, db.Exec(`
		INSERT INTO branches (id, created_at, updated_at, tenant_id,
		                      name, is_active)
		VALUES (?, datetime('now'), datetime('now'), ?, ?, 0)`,
		"br-a3", "tenant-a", "Archivada").Error)

	r := mountBranches(db, "tenant-a")
	w := doJSON(t, r, http.MethodGet, "/store/branches", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Data []models.Branch `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Data, 2,
		"only own tenant's active branches should surface")

	names := []string{body.Data[0].Name, body.Data[1].Name}
	assert.Contains(t, names, "Sede Norte")
	assert.Contains(t, names, "Sede Sur")
	assert.NotContains(t, names, "Sede de Otro Tenant")
	assert.NotContains(t, names, "Archivada")
}

func TestListBranches_RejectsWhenNoTenant(t *testing.T) {
	db := setupBranchesDB(t)
	r := mountBranches(db, "") // no tenant in context
	w := doJSON(t, r, http.MethodGet, "/store/branches", nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestCreateBranch_HappyPath(t *testing.T) {
	db := setupBranchesDB(t)
	r := mountBranches(db, "tenant-a")

	w := doJSON(t, r, http.MethodPost, "/store/branches", map[string]string{
		"name":    "Sede Centro",
		"address": "Cra 10 #20-30",
	})
	require.Equal(t, http.StatusCreated, w.Code)

	var body struct {
		Data models.Branch `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "Sede Centro", body.Data.Name)
	assert.Equal(t, "tenant-a", body.Data.TenantID)
	assert.True(t, body.Data.IsActive)
}

func TestCreateBranch_RejectsWhitespaceOnlyName(t *testing.T) {
	db := setupBranchesDB(t)
	r := mountBranches(db, "tenant-a")

	w := doJSON(t, r, http.MethodPost, "/store/branches", map[string]string{
		"name": "    ",
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpdateBranch_PartialPatchHappyPath(t *testing.T) {
	db := setupBranchesDB(t)
	seedTenantAndBranch(t, db, "tenant-a", "br-a1", "Original")
	r := mountBranches(db, "tenant-a")

	newName := "Sede Renombrada"
	w := doJSON(t, r, http.MethodPatch, "/store/branches/br-a1",
		map[string]any{"name": newName})
	require.Equal(t, http.StatusOK, w.Code)

	var updated models.Branch
	require.NoError(t, db.Where("id = ?", "br-a1").First(&updated).Error)
	assert.Equal(t, newName, updated.Name)
}

func TestUpdateBranch_CrossTenantReturnsNotFound(t *testing.T) {
	// A crafted PATCH from tenant-b against tenant-a's branch must
	// look the same as a non-existent branch — 404, not 403 — to
	// avoid leaking the ID's existence.
	db := setupBranchesDB(t)
	seedTenantAndBranch(t, db, "tenant-a", "br-a1", "De Tenant A")
	r := mountBranches(db, "tenant-b")

	w := doJSON(t, r, http.MethodPatch, "/store/branches/br-a1",
		map[string]any{"name": "hijacked"})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteBranch_RejectsWhenLastActive(t *testing.T) {
	// A tenant must keep at least one active sede so employees and
	// inventory still have a scope. Deleting the last one is a 400.
	db := setupBranchesDB(t)
	seedTenantAndBranch(t, db, "tenant-a", "br-only", "Única")
	r := mountBranches(db, "tenant-a")

	w := doJSON(t, r, http.MethodDelete, "/store/branches/br-only", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "last_branch")
}

func TestDeleteBranch_SucceedsWhenOtherBranchExists(t *testing.T) {
	db := setupBranchesDB(t)
	seedTenantAndBranch(t, db, "tenant-a", "br-keep", "Se queda")
	seedTenantAndBranch(t, db, "tenant-a", "br-gone", "Se va")
	r := mountBranches(db, "tenant-a")

	w := doJSON(t, r, http.MethodDelete, "/store/branches/br-gone", nil)
	assert.Equal(t, http.StatusOK, w.Code)

	// Soft delete: the row is gone from the default-scoped query,
	// but Unscoped() still finds it with deleted_at populated.
	var gone models.Branch
	err := db.Where("id = ?", "br-gone").First(&gone).Error
	assert.Error(t, err, "soft-deleted row is filtered by default scope")
}

func TestDeleteBranch_CrossTenantReturnsNotFound(t *testing.T) {
	db := setupBranchesDB(t)
	seedTenantAndBranch(t, db, "tenant-a", "br-1", "A1")
	seedTenantAndBranch(t, db, "tenant-a", "br-2", "A2")
	seedTenantAndBranch(t, db, "tenant-b", "br-bx", "De B")
	r := mountBranches(db, "tenant-a")

	w := doJSON(t, r, http.MethodDelete, "/store/branches/br-bx", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
