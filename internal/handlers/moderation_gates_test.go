// Spec: specs/104-moderacion-f1-lexico/spec.md
package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupModerationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Tenant{}, &models.Product{}, &models.Promotion{},
		&models.TenantPaymentMethod{}, &models.TenantCatalogConfig{},
		&models.ProductMedia{}, &models.ModerationLog{},
	))
	return db
}

func publicCatalogGET(t *testing.T, db *gorm.DB, slug string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/store/:slug/catalog", handlers.PublicCatalog(db))
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/store/%s/catalog", slug), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// AC-01 + AC-02: un producto prohibido se GUARDA (es el inventario del
// tendero) pero con moderation_status=blocked, queda registro auditable, y
// NO sale en el catálogo público.
func TestModeracion_ProductoProhibido_SeGuardaBloqueadoYFueraDelCatalogo(t *testing.T) {
	db := setupModerationDB(t)
	tenant := models.Tenant{BaseModel: models.BaseModel{ID: "t1"}, BusinessName: "Tienda", Phone: "300", StoreSlug: strPtr("tienda-mod")}
	require.NoError(t, db.Create(&tenant).Error)

	polvora := models.Product{TenantID: "t1", Name: "Volador pólvora x12", Price: 5000}
	arroz := models.Product{TenantID: "t1", Name: "Arroz Diana 500g", Price: 3000}
	require.NoError(t, db.Create(&polvora).Error)
	require.NoError(t, db.Create(&arroz).Error)

	// El hook BeforeSave clasificó al guardar.
	var saved models.Product
	require.NoError(t, db.First(&saved, "id = ?", polvora.ID).Error)
	assert.Equal(t, "blocked", saved.ModerationStatus, "producto prohibido debe quedar blocked")
	assert.Equal(t, "polvora", saved.ModerationCategory)

	// Registro auditable.
	var logs int64
	db.Model(&models.ModerationLog{}).Where("entity_id = ? AND verdict = 'blocked'", polvora.ID).Count(&logs)
	assert.Equal(t, int64(1), logs, "debe quedar moderation_log")

	// Catálogo público: solo el arroz.
	w := publicCatalogGET(t, db, "tienda-mod")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := w.Body.String()
	assert.Contains(t, body, "Arroz Diana")
	assert.NotContains(t, body, "Volador", "el producto blocked no puede salir al catálogo público")
}

// AC-03: tabaco queda blocked (Ley 1335: prohibida su publicidad) y OTC review.
func TestModeracion_TabacoBloqueado_OTCEnRevision(t *testing.T) {
	db := setupModerationDB(t)
	marlboro := models.Product{TenantID: "t1", Name: "Marlboro rojo x20", Price: 12000}
	dolex := models.Product{TenantID: "t1", Name: "Acetaminofén 500mg", Price: 2000}
	require.NoError(t, db.Create(&marlboro).Error)
	require.NoError(t, db.Create(&dolex).Error)

	var m, d models.Product
	db.First(&m, "id = ?", marlboro.ID)
	db.First(&d, "id = ?", dolex.ID)
	assert.Equal(t, "blocked", m.ModerationStatus)
	assert.Equal(t, "tabaco", m.ModerationCategory)
	assert.Equal(t, "review", d.ModerationStatus)
	assert.Equal(t, "medicamentos", d.ModerationCategory)
}

// AC-06: tenant con catalog_suspended_at → catálogo público no disponible.
func TestModeracion_CatalogoSuspendido_NoDisponible(t *testing.T) {
	db := setupModerationDB(t)
	now := time.Now()
	tenant := models.Tenant{BaseModel: models.BaseModel{ID: "t2"}, BusinessName: "Suspendida", Phone: "301", StoreSlug: strPtr("tienda-susp"), CatalogSuspendedAt: &now}
	require.NoError(t, db.Create(&tenant).Error)

	w := publicCatalogGET(t, db, "tienda-susp")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "no disponible")
}

// AC-07 (backfill se prueba vía EnsureProductModeration en el camino por
// mapa): un rename por Updates(map) que introduce un término prohibido
// queda cubierto por la re-evaluación explícita.
func TestModeracion_UpdatePorMapa_ReEvalua(t *testing.T) {
	db := setupModerationDB(t)
	p := models.Product{TenantID: "t1", Name: "Gaseosa 350ml", Price: 2500}
	require.NoError(t, db.Create(&p).Error)

	var before models.Product
	db.First(&before, "id = ?", p.ID)
	require.Equal(t, "allowed", before.ModerationStatus)

	// Update por mapa (como UpdateProduct y el sync): el hook no persiste.
	require.NoError(t, db.Model(&models.Product{}).Where("id = ?", p.ID).
		UpdateColumns(map[string]any{"name": "Cigarrillos sueltos"}).Error)

	// La re-evaluación explícita corrige el veredicto.
	services.EnsureProductModeration(db, "t1", p.ID)
	var after models.Product
	db.First(&after, "id = ?", p.ID)
	assert.Equal(t, "blocked", after.ModerationStatus)
	assert.Equal(t, "tabaco", after.ModerationCategory)
}

// AC-08: pedido público con producto blocked → 422.
func TestModeracion_PedidoConProductoBloqueado_Rechaza(t *testing.T) {
	db := setupOnlineOrdersDB(t)
	tenantID, _ := seedTenantWithBranch(t, db, "tienda-cuatro")
	blockedID := "11111111-1111-4111-8111-111111111111"
	require.NoError(t, db.Exec(
		`INSERT INTO products (id, tenant_id, name, moderation_status, created_at)
		 VALUES (?, ?, 'Marlboro rojo', 'blocked', datetime('now'))`, blockedID, tenantID,
	).Error)

	w := postOnlineOrder(t, db, "tienda-cuatro", map[string]any{
		"customer_name": "Cliente",
		"items": []map[string]any{{
			"product_id": blockedID, "name": "Marlboro rojo", "quantity": 1, "price": 12000,
		}},
	})
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "product_not_sellable_online", resp["code"])
}

// AC-05: una promo de difusión con contenido prohibido (tabaco) NO se crea —
// fail-closed duro (Ley 1335/2009: publicitar tabaco es ilegal de plano).
func TestModeracion_PromoConTabaco_NoSeCrea(t *testing.T) {
	db := setupPromoDB(t)
	require.NoError(t, db.AutoMigrate(&models.ModerationLog{}))
	r := promoRouter(db, "tenant-promo")

	w := doJSON(t, r, http.MethodPost, "/api/v1/broadcast-promotions", map[string]any{
		"title":            "Cigarrillos 2x1 este fin de semana",
		"message_template": "Aproveche la promo",
	})
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "moderation_blocked")

	var n int64
	db.Model(&models.BroadcastPromotion{}).Count(&n)
	assert.Equal(t, int64(0), n, "la promo bloqueada no debe existir")

	var logs int64
	db.Model(&models.ModerationLog{}).Where("entity_type = 'broadcast_promotion'").Count(&logs)
	assert.Equal(t, int64(1), logs, "debe quedar registro auditable del rechazo")

	// El mismo tendero con una promo limpia sí puede crear.
	w2 := doJSON(t, r, http.MethodPost, "/api/v1/broadcast-promotions", map[string]any{
		"title":            "Cerveza fría 2x1",
		"message_template": "Solo hoy",
	})
	require.Equal(t, http.StatusCreated, w2.Code, w2.Body.String())
}
