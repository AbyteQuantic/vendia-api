// Reporte real del fundador: el selector de sede en el creador de combos
// (PromoBuilderScreen) ya existía en pantalla pero era puramente decorativo
// — ni el listado ni la creación de combos filtraban/asignaban por
// sucursal. Concilio (Workflow) decidió replicar el patrón ya probado de
// Product.BranchID (nullable: nil = todas las sedes) en Promotion, scopeando
// List/Create con el mismo helper ResolveBranchScope/ApplyBranchScope que ya
// usan analytics/inventario.
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

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

func setupPromotionBranchDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Branch{}, &models.Product{}, &models.Promotion{}, &models.PromotionItem{},
	))
	return db
}

func seedPromoBranch(t *testing.T, db *gorm.DB, tenantID, name string) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Create(&models.Branch{
		BaseModel: models.BaseModel{ID: id},
		TenantID:  tenantID, Name: name, IsActive: true,
	}).Error)
	return id
}

func mountPromotions(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	})
	r.GET("/promotions", handlers.ListPromotions(db))
	r.POST("/promotions", handlers.CreatePromotion(db))
	return r
}

// ── CreatePromotion ──────────────────────────────────────────────────────

func TestCreatePromotion_NoBranchParam_CreatesGlobalPromotion(t *testing.T) {
	db := setupPromotionBranchDB(t)
	const tenant = "tenant-mono-sede"
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "prod-1"}, TenantID: tenant,
		Name: "Coca-Cola", Price: 2500,
	}).Error)
	r := mountPromotions(db, tenant)

	w := doJSON(t, r, http.MethodPost, "/promotions", map[string]any{
		"id":         "promo-global",
		"name":       "2x1 en gaseosas",
		"promo_type": "combo",
		"items": []map[string]any{
			{"product_id": "prod-1", "quantity": 2, "promo_price": 2500},
		},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var promo models.Promotion
	require.NoError(t, db.First(&promo, "id = ?", "promo-global").Error)
	assert.Nil(t, promo.BranchID,
		"sin ?branch_id= el combo debe quedar GLOBAL (nil) — comportamiento preexistente")
}

func TestCreatePromotion_WithBranchParam_ScopesToThatBranch(t *testing.T) {
	db := setupPromotionBranchDB(t)
	const tenant = "tenant-multi-sede"
	branchA := seedPromoBranch(t, db, tenant, "Sede Norte")
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "prod-1"}, TenantID: tenant,
		Name: "Coca-Cola", Price: 2500,
	}).Error)
	r := mountPromotions(db, tenant)

	w := doJSON(t, r, http.MethodPost, "/promotions?branch_id="+branchA, map[string]any{
		"id":         "promo-sede-a",
		"name":       "2x1 en gaseosas — Sede Norte",
		"promo_type": "combo",
		"items": []map[string]any{
			{"product_id": "prod-1", "quantity": 2, "promo_price": 2500},
		},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var promo models.Promotion
	require.NoError(t, db.First(&promo, "id = ?", "promo-sede-a").Error)
	require.NotNil(t, promo.BranchID, "el combo debe quedar scopeado a la sede activa")
	assert.Equal(t, branchA, *promo.BranchID)
}

func TestCreatePromotion_BranchNotOwnedByTenant_Returns403(t *testing.T) {
	db := setupPromotionBranchDB(t)
	otherTenantBranch := seedPromoBranch(t, db, "otro-tenant", "Sede Ajena")
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: "prod-1"}, TenantID: "tenant-victima",
		Name: "Coca-Cola", Price: 2500,
	}).Error)
	r := mountPromotions(db, "tenant-victima")

	w := doJSON(t, r, http.MethodPost, "/promotions?branch_id="+otherTenantBranch, map[string]any{
		"id":         "promo-intrusa",
		"name":       "Combo",
		"promo_type": "combo",
		"items": []map[string]any{
			{"product_id": "prod-1", "quantity": 1, "promo_price": 2000},
		},
	})
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	var count int64
	db.Model(&models.Promotion{}).Where("id = ?", "promo-intrusa").Count(&count)
	assert.Zero(t, count, "no debe crearse ningún combo cuando la sede no pertenece al tenant")
}

// ── ListPromotions ───────────────────────────────────────────────────────

func TestListPromotions_BranchScope_ShowsOwnAndGlobalNeverOtherBranch(t *testing.T) {
	db := setupPromotionBranchDB(t)
	const tenant = "tenant-multi-sede"
	branchA := seedPromoBranch(t, db, tenant, "Sede Norte")
	branchB := seedPromoBranch(t, db, tenant, "Sede Sur")

	mk := func(id string, branchID *string) models.Promotion {
		return models.Promotion{
			BaseModel: models.BaseModel{ID: id}, TenantID: tenant, BranchID: branchID,
			Name: "Combo " + id, PromoType: "combo", IsActive: true,
		}
	}
	require.NoError(t, db.Create(&[]models.Promotion{
		mk("promo-a", &branchA),
		mk("promo-b", &branchB),
		mk("promo-global", nil),
	}).Error)

	r := mountPromotions(db, tenant)

	// Sede A ve su combo + el global, nunca el de sede B.
	w := doJSON(t, r, http.MethodGet, "/promotions?branch_id="+branchA, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	names := promotionIDs(t, w.Body.Bytes())
	assert.ElementsMatch(t, []string{"promo-a", "promo-global"}, names)

	// Sede B ve su combo + el global, nunca el de sede A.
	w = doJSON(t, r, http.MethodGet, "/promotions?branch_id="+branchB, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	names = promotionIDs(t, w.Body.Bytes())
	assert.ElementsMatch(t, []string{"promo-b", "promo-global"}, names)

	// Sin sede seleccionada (tenant mono-sede o llamada sin scope): ve TODO,
	// igual que el comportamiento preexistente — nunca esconde nada.
	w = doJSON(t, r, http.MethodGet, "/promotions", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	names = promotionIDs(t, w.Body.Bytes())
	assert.ElementsMatch(t, []string{"promo-a", "promo-b", "promo-global"}, names)
}

func promotionIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var resp struct {
		Data []models.Promotion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	ids := make([]string, 0, len(resp.Data))
	for _, p := range resp.Data {
		ids = append(ids, p.ID)
	}
	return ids
}
