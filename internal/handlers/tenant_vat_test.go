package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupTenantVATDB spins up an in-memory sqlite with the Tenant table
// migrated from GORM. The Tenant model auto-migrates fine in sqlite as
// long as we never request the gen_random_uuid() default — we don't,
// because we always seed the ID explicitly.
func setupTenantVATDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Tenant{}))
	return db
}

// mountTenantVAT wires the two handlers under test behind a fake auth
// middleware that just plants the tenantID directly in the gin context.
// Pass an empty tenantID to simulate an unauthenticated request — the
// handler must reject with 401.
func mountTenantVAT(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if tenantID != "" {
			c.Set(middleware.TenantIDKey, tenantID)
		}
		c.Next()
	})
	r.GET("/tenant/vat", GetTenantVATSettings(db))
	r.PATCH("/tenant/vat", UpdateTenantVATSettings(db))
	return r
}

// patchJSON is the PATCH counterpart of the existing postJSON / getJSON
// helpers. Defined locally so the file is self-contained and we avoid
// touching unrelated test files.
func patchJSON(r http.Handler, path string, body any) *httptest.ResponseRecorder {
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPatch, path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// seedTenantVAT creates a minimal Tenant row keyed by id. We can't use
// the shared seedTenant helper from customer_consent_test.go because
// it forces a non-nil StoreSlug with a UNIQUE index, which would
// collide across the multi-test cases here.
func seedTenantVAT(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	tenant := models.Tenant{
		BaseModel:    models.BaseModel{ID: id},
		BusinessName: "VAT Tenant " + id,
		Phone:        "vat-" + id,
	}
	require.NoError(t, db.Create(&tenant).Error)
}

func TestTenantVAT_Patch_EnablesAndStampsActivatedAt(t *testing.T) {
	db := setupTenantVATDB(t)
	seedTenantVAT(t, db, "tenant-vat-1")
	r := mountTenantVAT(db, "tenant-vat-1")

	enabled := true
	rate := 0.19
	w := patchJSON(r, "/tenant/vat", map[string]any{
		"vat_enabled": enabled,
		"vat_rate":    rate,
	})
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var tenant models.Tenant
	require.NoError(t, db.Where("id = ?", "tenant-vat-1").First(&tenant).Error)
	require.NotNil(t, tenant.VATEnabled)
	assert.True(t, *tenant.VATEnabled)
	require.NotNil(t, tenant.VATRate)
	assert.InDelta(t, 0.19, *tenant.VATRate, 1e-9)
	require.NotNil(t, tenant.VATActivatedAt,
		"first time we set vat_enabled=true must stamp vat_activated_at")
}

func TestTenantVAT_Patch_SecondActivationDoesNotOverwriteActivatedAt(t *testing.T) {
	db := setupTenantVATDB(t)
	seedTenantVAT(t, db, "tenant-vat-2")
	r := mountTenantVAT(db, "tenant-vat-2")

	// First activation — VATActivatedAt gets stamped.
	enabled := true
	w1 := patchJSON(r, "/tenant/vat", map[string]any{"vat_enabled": enabled})
	require.Equal(t, http.StatusOK, w1.Code, "body=%s", w1.Body.String())

	var afterFirst models.Tenant
	require.NoError(t, db.Where("id = ?", "tenant-vat-2").First(&afterFirst).Error)
	require.NotNil(t, afterFirst.VATActivatedAt)
	firstActivatedAt := *afterFirst.VATActivatedAt

	// Add a tangible delay so any accidental overwrite would be visible.
	time.Sleep(10 * time.Millisecond)

	// Second activation — same payload. The "una vez activado, siempre
	// activo" rule says VATActivatedAt is immutable once stamped.
	w2 := patchJSON(r, "/tenant/vat", map[string]any{"vat_enabled": enabled})
	require.Equal(t, http.StatusOK, w2.Code, "body=%s", w2.Body.String())

	var afterSecond models.Tenant
	require.NoError(t, db.Where("id = ?", "tenant-vat-2").First(&afterSecond).Error)
	require.NotNil(t, afterSecond.VATActivatedAt)
	assert.True(t, afterSecond.VATActivatedAt.Equal(firstActivatedAt),
		"vat_activated_at must NOT be overwritten on subsequent activations")
}

func TestTenantVAT_Patch_RejectsNegativeRate(t *testing.T) {
	db := setupTenantVATDB(t)
	seedTenantVAT(t, db, "tenant-vat-3")
	r := mountTenantVAT(db, "tenant-vat-3")

	rate := -0.1
	w := patchJSON(r, "/tenant/vat", map[string]any{"vat_rate": rate})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestTenantVAT_Patch_RejectsRateAboveCap(t *testing.T) {
	db := setupTenantVATDB(t)
	seedTenantVAT(t, db, "tenant-vat-4")
	r := mountTenantVAT(db, "tenant-vat-4")

	rate := 0.6
	w := patchJSON(r, "/tenant/vat", map[string]any{"vat_rate": rate})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestTenantVAT_Patch_RejectsNegativeThreshold(t *testing.T) {
	db := setupTenantVATDB(t)
	seedTenantVAT(t, db, "tenant-vat-5")
	r := mountTenantVAT(db, "tenant-vat-5")

	threshold := int64(-1)
	w := patchJSON(r, "/tenant/vat", map[string]any{"dian_threshold_cop": threshold})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestTenantVAT_Patch_RejectsWhenNoTenantInContext(t *testing.T) {
	db := setupTenantVATDB(t)
	r := mountTenantVAT(db, "") // no tenant in context

	enabled := true
	w := patchJSON(r, "/tenant/vat", map[string]any{"vat_enabled": enabled})
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestTenantVAT_Get_ReturnsDefaultsForNewTenant(t *testing.T) {
	db := setupTenantVATDB(t)
	seedTenantVAT(t, db, "tenant-vat-6")
	r := mountTenantVAT(db, "tenant-vat-6")

	w := getJSON(r, "/tenant/vat")
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp struct {
		Data struct {
			VATEnabled          bool       `json:"vat_enabled"`
			VATRate             float64    `json:"vat_rate"`
			VATInclusivePricing bool       `json:"vat_inclusive_pricing"`
			VATActivatedAt      *time.Time `json:"vat_activated_at"`
			DIANThresholdCOP    int64      `json:"dian_threshold_cop"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.False(t, resp.Data.VATEnabled,
		"a brand-new tenant must report vat_enabled=false")
	assert.True(t, resp.Data.VATInclusivePricing,
		"vat_inclusive_pricing default = true (Colombian convention)")
	assert.Equal(t, float64(0), resp.Data.VATRate)
	assert.Equal(t, int64(0), resp.Data.DIANThresholdCOP)
	assert.Nil(t, resp.Data.VATActivatedAt)
}
