// Spec: specs/033-difusion-promociones/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
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

// ── F033 — broadcast promotions handler suite (in-memory SQLite) ────────────

func setupPromoDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Tenant{},
		&models.Customer{},
		&models.Product{},
		&models.Sale{},
		&models.BroadcastPromotion{},
		&models.BroadcastPromotionItem{},
		&models.BroadcastPromotionDelivery{},
	))
	// Notifications uses a Postgres-only gen_random_uuid() default that
	// SQLite can't parse; stand up an equivalent table by hand (the id
	// is filled by the model's BeforeCreate hook). The promotions-push
	// job writes here.
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS notifications (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			tenant_id TEXT NOT NULL,
			title TEXT NOT NULL,
			body TEXT DEFAULT '',
			type TEXT DEFAULT 'info',
			is_read INTEGER DEFAULT 0,
			deep_link TEXT,
			pushed_at DATETIME,
			dedup_key TEXT
		)
	`).Error)
	return db
}

// promoRouter wires the JWT-protected promotion endpoints with the
// tenant id injected as the auth middleware would.
func promoRouter(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	inject := func(c *gin.Context) {
		c.Set(middleware.TenantIDKey, tenantID)
		c.Next()
	}
	g := r.Group("/api/v1", inject)
	g.GET("/broadcast-promotions", handlers.ListBroadcastPromotions(db))
	g.POST("/broadcast-promotions", handlers.CreateBroadcastPromotion(db))
	g.GET("/broadcast-promotions/:id", handlers.GetBroadcastPromotion(db))
	g.PATCH("/broadcast-promotions/:id", handlers.UpdateBroadcastPromotion(db))
	g.DELETE("/broadcast-promotions/:id", handlers.DeleteBroadcastPromotion(db))
	g.POST("/broadcast-promotions/:id/audience", handlers.BroadcastPromotionAudience(db))
	g.POST("/broadcast-promotions/:id/deliveries", handlers.CreateBroadcastDeliveries(db))
	g.PATCH("/broadcast-promotions/:id/deliveries/:deliveryId", handlers.UpdateBroadcastDelivery(db))
	return r
}

// publicPromoRouter wires the unauthenticated public endpoints.
func publicPromoRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/public/broadcast-promotions/:token", handlers.GetPublicBroadcastPromotion(db))
	r.POST("/api/v1/public/broadcast-promotions/:token/visit", handlers.VisitPublicBroadcastPromotion(db))
	return r
}

// doJSON is the shared test helper defined in branches_test.go.

func validPromoPayload() map[string]any {
	return map[string]any{
		"title":            "20% en kits de baño",
		"description":      "Solo esta semana",
		"message_template": "Hola {primer_nombre} 👋 tenemos promo {link}",
		"valid_from":       time.Now().UTC().Format(time.RFC3339),
		"valid_until":      time.Now().UTC().AddDate(0, 0, 7).Format(time.RFC3339),
	}
}

// ── T-06 — CRUD ─────────────────────────────────────────────────────────────

func TestCreateBroadcastPromotion_HappyPath(t *testing.T) {
	db := setupPromoDB(t)
	r := promoRouter(db, "tenant-1")

	w := doJSON(t, r, "POST", "/api/v1/broadcast-promotions", validPromoPayload())
	require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())

	var resp struct {
		Data models.BroadcastPromotion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data.ID)
	assert.NotEmpty(t, resp.Data.PublicToken, "debe generar un public_token")
	assert.True(t, models.IsValidUUID(resp.Data.PublicToken), "public_token es UUID v4")
	assert.Equal(t, "20% en kits de baño", resp.Data.Title)
}

func TestCreateBroadcastPromotion_RejectsEmptyTitle(t *testing.T) {
	db := setupPromoDB(t)
	r := promoRouter(db, "tenant-1")

	p := validPromoPayload()
	p["title"] = ""
	w := doJSON(t, r, "POST", "/api/v1/broadcast-promotions", p)
	assert.Equal(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
}

func TestCreateBroadcastPromotion_RejectsInvalidVigencia(t *testing.T) {
	db := setupPromoDB(t)
	r := promoRouter(db, "tenant-1")

	p := validPromoPayload()
	// valid_until before valid_from.
	p["valid_from"] = time.Now().UTC().AddDate(0, 0, 7).Format(time.RFC3339)
	p["valid_until"] = time.Now().UTC().Format(time.RFC3339)
	w := doJSON(t, r, "POST", "/api/v1/broadcast-promotions", p)
	assert.Equal(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
}

func TestListBroadcastPromotions_TenantScoped(t *testing.T) {
	db := setupPromoDB(t)
	require.Equal(t, http.StatusCreated,
		doJSON(t, promoRouter(db, "tenant-1"), "POST", "/api/v1/broadcast-promotions", validPromoPayload()).Code)
	require.Equal(t, http.StatusCreated,
		doJSON(t, promoRouter(db, "tenant-2"), "POST", "/api/v1/broadcast-promotions", validPromoPayload()).Code)

	w := doJSON(t, promoRouter(db, "tenant-1"), "GET", "/api/v1/broadcast-promotions", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data []models.BroadcastPromotion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp.Data, 1, "tenant-1 solo ve su propia promo")
}

func TestUpdateBroadcastPromotion_BlockedWhenSentDeliveriesExist(t *testing.T) {
	db := setupPromoDB(t)
	r := promoRouter(db, "tenant-1")

	w := doJSON(t, r, "POST", "/api/v1/broadcast-promotions", validPromoPayload())
	require.Equal(t, http.StatusCreated, w.Code)
	var created struct {
		Data models.BroadcastPromotion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	promoID := created.Data.ID

	// Seed a customer and a SENT delivery directly.
	cust := models.Customer{TenantID: "tenant-1", Name: "Maria", Phone: "3001"}
	require.NoError(t, db.Create(&cust).Error)
	sentAt := time.Now().UTC()
	require.NoError(t, db.Create(&models.BroadcastPromotionDelivery{
		PromotionID: promoID,
		CustomerID:  cust.ID,
		Channel:     models.PromotionChannelWhatsApp,
		Status:      models.PromotionDeliverySent,
		SentAt:      &sentAt,
	}).Error)

	// PATCH must be refused — what was sent cannot be edited.
	patch := doJSON(t, r, "PATCH", "/api/v1/broadcast-promotions/"+promoID,
		map[string]any{"title": "Título nuevo"})
	assert.Equal(t, http.StatusConflict, patch.Code, "body=%s", patch.Body.String())
}

func TestUpdateBroadcastPromotion_AllowedWithoutSentDeliveries(t *testing.T) {
	db := setupPromoDB(t)
	r := promoRouter(db, "tenant-1")

	w := doJSON(t, r, "POST", "/api/v1/broadcast-promotions", validPromoPayload())
	var created struct {
		Data models.BroadcastPromotion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	patch := doJSON(t, r, "PATCH", "/api/v1/broadcast-promotions/"+created.Data.ID,
		map[string]any{"title": "Título editado"})
	assert.Equal(t, http.StatusOK, patch.Code, "body=%s", patch.Body.String())
}

func TestDeleteBroadcastPromotion_CascadesItemsAndDeliveries(t *testing.T) {
	db := setupPromoDB(t)
	r := promoRouter(db, "tenant-1")

	w := doJSON(t, r, "POST", "/api/v1/broadcast-promotions", validPromoPayload())
	var created struct {
		Data models.BroadcastPromotion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	promoID := created.Data.ID

	require.NoError(t, db.Create(&models.BroadcastPromotionItem{
		PromotionID: promoID, ProductID: "p-1",
	}).Error)
	cust := models.Customer{TenantID: "tenant-1", Name: "Ana", Phone: "3002"}
	require.NoError(t, db.Create(&cust).Error)
	require.NoError(t, db.Create(&models.BroadcastPromotionDelivery{
		PromotionID: promoID, CustomerID: cust.ID,
		Channel: models.PromotionChannelLink, Status: models.PromotionDeliveryQueued,
	}).Error)

	del := doJSON(t, r, "DELETE", "/api/v1/broadcast-promotions/"+promoID, nil)
	require.Equal(t, http.StatusOK, del.Code, "body=%s", del.Body.String())

	var itemCount, deliveryCount int64
	db.Model(&models.BroadcastPromotionItem{}).Where("promotion_id = ?", promoID).Count(&itemCount)
	db.Model(&models.BroadcastPromotionDelivery{}).Where("promotion_id = ?", promoID).Count(&deliveryCount)
	assert.EqualValues(t, 0, itemCount, "items eliminados en cascada")
	assert.EqualValues(t, 0, deliveryCount, "deliveries eliminados en cascada")
}

// ── T-08 — POST /audience ───────────────────────────────────────────────────

func seedAudienceCustomers(t *testing.T, db *gorm.DB, tenantID string) {
	t.Helper()
	now := time.Now().UTC()
	mk := func(id, name, phone string, sales []struct {
		amt  float64
		days int
	}) {
		c := models.Customer{
			BaseModel: models.BaseModel{ID: id}, TenantID: tenantID,
			Name: name, Phone: phone,
		}
		require.NoError(t, db.Create(&c).Error)
		for i, s := range sales {
			cid := id
			require.NoError(t, db.Create(&models.Sale{
				BaseModel: models.BaseModel{ID: id + "-s" + time.Duration(i).String(), CreatedAt: now.AddDate(0, 0, -s.days)},
				TenantID:  tenantID, Total: s.amt, CustomerID: &cid,
			}).Error)
		}
	}
	type s = struct {
		amt  float64
		days int
	}
	mk("ac1", "Maria", "3001", []s{{50000, 1}, {40000, 5}, {30000, 12}}) // frequent + recent
	mk("ac2", "Pedro", "3002", nil)                                      // dormant
	mk("ac3", "Ana", "", []s{{99000, 1}})                                // no phone — excluded
}

func TestBroadcastPromotionAudience_Filters(t *testing.T) {
	db := setupPromoDB(t)
	r := promoRouter(db, "tenant-1")
	seedAudienceCustomers(t, db, "tenant-1")

	w := doJSON(t, r, "POST", "/api/v1/broadcast-promotions", validPromoPayload())
	var created struct {
		Data models.BroadcastPromotion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	promoID := created.Data.ID

	cases := []struct {
		filter  string
		wantIDs []string
	}{
		{"all", []string{"ac1", "ac2"}},
		{"frequent", []string{"ac1"}},
		{"dormant", []string{"ac2"}},
		{"recent", []string{"ac1"}},
	}
	for _, tc := range cases {
		t.Run(tc.filter, func(t *testing.T) {
			resp := doJSON(t, r, "POST", "/api/v1/broadcast-promotions/"+promoID+"/audience",
				map[string]any{"filter": tc.filter})
			require.Equal(t, http.StatusOK, resp.Code, "body=%s", resp.Body.String())
			var body struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
				Meta struct {
					Count int `json:"count"`
				} `json:"meta"`
			}
			require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
			got := make([]string, 0)
			for _, d := range body.Data {
				got = append(got, d.ID)
			}
			assert.ElementsMatch(t, tc.wantIDs, got)
			assert.Equal(t, len(tc.wantIDs), body.Meta.Count, "count coincide")
		})
	}
}

func TestBroadcastPromotionAudience_Manual(t *testing.T) {
	db := setupPromoDB(t)
	r := promoRouter(db, "tenant-1")
	seedAudienceCustomers(t, db, "tenant-1")

	w := doJSON(t, r, "POST", "/api/v1/broadcast-promotions", validPromoPayload())
	var created struct {
		Data models.BroadcastPromotion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	resp := doJSON(t, r, "POST", "/api/v1/broadcast-promotions/"+created.Data.ID+"/audience",
		map[string]any{"filter": "manual", "customer_ids": []string{"ac1"}})
	require.Equal(t, http.StatusOK, resp.Code, "body=%s", resp.Body.String())
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
	require.Len(t, body.Data, 1)
	assert.Equal(t, "ac1", body.Data[0].ID)
}

// ── T-10 — deliveries ───────────────────────────────────────────────────────

func TestCreateBroadcastDeliveries_QueuesAndDedups(t *testing.T) {
	db := setupPromoDB(t)
	r := promoRouter(db, "tenant-1")
	seedAudienceCustomers(t, db, "tenant-1")

	w := doJSON(t, r, "POST", "/api/v1/broadcast-promotions", validPromoPayload())
	var created struct {
		Data models.BroadcastPromotion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	promoID := created.Data.ID

	body := map[string]any{
		"customer_ids": []string{"ac1", "ac2"},
		"channel":      "whatsapp",
	}
	resp := doJSON(t, r, "POST", "/api/v1/broadcast-promotions/"+promoID+"/deliveries", body)
	require.Equal(t, http.StatusCreated, resp.Code, "body=%s", resp.Body.String())
	var dr struct {
		Data []models.BroadcastPromotionDelivery `json:"data"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &dr))
	assert.Len(t, dr.Data, 2)
	for _, d := range dr.Data {
		assert.Equal(t, models.PromotionDeliveryQueued, d.Status)
	}

	// Re-queue the same audience on the same channel — uniqueness must
	// keep the total at 2, no duplicates.
	resp2 := doJSON(t, r, "POST", "/api/v1/broadcast-promotions/"+promoID+"/deliveries", body)
	require.Equal(t, http.StatusCreated, resp2.Code, "body=%s", resp2.Body.String())

	var total int64
	db.Model(&models.BroadcastPromotionDelivery{}).Where("promotion_id = ?", promoID).Count(&total)
	assert.EqualValues(t, 2, total, "uniqueness evita deliveries duplicados")
}

func TestCreateBroadcastDeliveries_RejectsInvalidChannel(t *testing.T) {
	db := setupPromoDB(t)
	r := promoRouter(db, "tenant-1")
	seedAudienceCustomers(t, db, "tenant-1")

	w := doJSON(t, r, "POST", "/api/v1/broadcast-promotions", validPromoPayload())
	var created struct {
		Data models.BroadcastPromotion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	resp := doJSON(t, r, "POST", "/api/v1/broadcast-promotions/"+created.Data.ID+"/deliveries",
		map[string]any{"customer_ids": []string{"ac1"}, "channel": "telegram"})
	assert.Equal(t, http.StatusBadRequest, resp.Code, "body=%s", resp.Body.String())
}

func TestUpdateBroadcastDelivery_MarksSent(t *testing.T) {
	db := setupPromoDB(t)
	r := promoRouter(db, "tenant-1")
	seedAudienceCustomers(t, db, "tenant-1")

	w := doJSON(t, r, "POST", "/api/v1/broadcast-promotions", validPromoPayload())
	var created struct {
		Data models.BroadcastPromotion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	promoID := created.Data.ID

	resp := doJSON(t, r, "POST", "/api/v1/broadcast-promotions/"+promoID+"/deliveries",
		map[string]any{"customer_ids": []string{"ac1"}, "channel": "whatsapp"})
	require.Equal(t, http.StatusCreated, resp.Code)
	var dr struct {
		Data []models.BroadcastPromotionDelivery `json:"data"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &dr))
	require.Len(t, dr.Data, 1)
	deliveryID := dr.Data[0].ID

	patch := doJSON(t, r, "PATCH",
		"/api/v1/broadcast-promotions/"+promoID+"/deliveries/"+deliveryID,
		map[string]any{"status": "sent"})
	require.Equal(t, http.StatusOK, patch.Code, "body=%s", patch.Body.String())

	var d models.BroadcastPromotionDelivery
	require.NoError(t, db.First(&d, "id = ?", deliveryID).Error)
	assert.Equal(t, models.PromotionDeliverySent, d.Status)
	require.NotNil(t, d.SentAt, "sent_at debe quedar marcado")
}

func TestUpdateBroadcastDelivery_RejectsInvalidStatus(t *testing.T) {
	db := setupPromoDB(t)
	r := promoRouter(db, "tenant-1")
	seedAudienceCustomers(t, db, "tenant-1")

	w := doJSON(t, r, "POST", "/api/v1/broadcast-promotions", validPromoPayload())
	var created struct {
		Data models.BroadcastPromotion `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	promoID := created.Data.ID

	resp := doJSON(t, r, "POST", "/api/v1/broadcast-promotions/"+promoID+"/deliveries",
		map[string]any{"customer_ids": []string{"ac1"}, "channel": "whatsapp"})
	var dr struct {
		Data []models.BroadcastPromotionDelivery `json:"data"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &dr))

	patch := doJSON(t, r, "PATCH",
		"/api/v1/broadcast-promotions/"+promoID+"/deliveries/"+dr.Data[0].ID,
		map[string]any{"status": "exploded"})
	assert.Equal(t, http.StatusBadRequest, patch.Code, "body=%s", patch.Body.String())
}

// ── T-12 — public endpoints ─────────────────────────────────────────────────

func seedPromoForPublic(t *testing.T, db *gorm.DB, tenantID string, from, until time.Time) models.BroadcastPromotion {
	t.Helper()
	require.NoError(t, db.Create(&models.Tenant{
		BaseModel: models.BaseModel{ID: tenantID}, OwnerName: "Dueño",
		Phone: "3000000000", PasswordHash: "x", BusinessName: "Tienda Test",
		SaleTypes: []string{"contado"},
	}).Error)
	promo := models.BroadcastPromotion{
		TenantID:    tenantID,
		Title:       "Promo pública",
		Description: "Detalle",
		ValidFrom:   from,
		ValidUntil:  until,
		PublicToken: "11111111-1111-1111-1111-111111111111",
		IsActive:    true,
	}
	require.NoError(t, db.Create(&promo).Error)
	return promo
}

func TestGetPublicBroadcastPromotion_ValidToken(t *testing.T) {
	db := setupPromoDB(t)
	r := publicPromoRouter(db)
	now := time.Now().UTC()
	seedPromoForPublic(t, db, "tenant-1", now.AddDate(0, 0, -1), now.AddDate(0, 0, 6))

	w := doJSON(t, r, "GET",
		"/api/v1/public/broadcast-promotions/11111111-1111-1111-1111-111111111111", nil)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var resp struct {
		Data struct {
			Title    string `json:"title"`
			Status   string `json:"status"`
			Business struct {
				Name string `json:"name"`
			} `json:"business"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "Promo pública", resp.Data.Title)
	assert.Equal(t, "active", resp.Data.Status)
	assert.Equal(t, "Tienda Test", resp.Data.Business.Name, "branding del tenant")
}

func TestGetPublicBroadcastPromotion_InvalidToken(t *testing.T) {
	db := setupPromoDB(t)
	r := publicPromoRouter(db)

	w := doJSON(t, r, "GET",
		"/api/v1/public/broadcast-promotions/99999999-9999-9999-9999-999999999999", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetPublicBroadcastPromotion_NotYetStarted(t *testing.T) {
	db := setupPromoDB(t)
	r := publicPromoRouter(db)
	now := time.Now().UTC()
	seedPromoForPublic(t, db, "tenant-1", now.AddDate(0, 0, 2), now.AddDate(0, 0, 9))

	w := doJSON(t, r, "GET",
		"/api/v1/public/broadcast-promotions/11111111-1111-1111-1111-111111111111", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "not_yet_started", resp.Data.Status)
}

func TestGetPublicBroadcastPromotion_Expired(t *testing.T) {
	db := setupPromoDB(t)
	r := publicPromoRouter(db)
	now := time.Now().UTC()
	seedPromoForPublic(t, db, "tenant-1", now.AddDate(0, 0, -10), now.AddDate(0, 0, -2))

	w := doJSON(t, r, "GET",
		"/api/v1/public/broadcast-promotions/11111111-1111-1111-1111-111111111111", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "expired", resp.Data.Status)
}

func TestVisitPublicBroadcastPromotion_IncrementsCountAndMarksDelivery(t *testing.T) {
	db := setupPromoDB(t)
	r := publicPromoRouter(db)
	now := time.Now().UTC()
	promo := seedPromoForPublic(t, db, "tenant-1", now.AddDate(0, 0, -1), now.AddDate(0, 0, 6))

	cust := models.Customer{TenantID: "tenant-1", Name: "Maria", Phone: "3001"}
	require.NoError(t, db.Create(&cust).Error)
	delivery := models.BroadcastPromotionDelivery{
		PromotionID: promo.ID, CustomerID: cust.ID,
		Channel: models.PromotionChannelWhatsApp, Status: models.PromotionDeliverySent,
	}
	require.NoError(t, db.Create(&delivery).Error)

	w := doJSON(t, r, "POST",
		"/api/v1/public/broadcast-promotions/"+promo.PublicToken+"/visit",
		map[string]any{"delivery_id": delivery.ID})
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var refreshed models.BroadcastPromotion
	require.NoError(t, db.First(&refreshed, "id = ?", promo.ID).Error)
	assert.EqualValues(t, 1, refreshed.VisitCount, "visit_count incrementado")

	var d models.BroadcastPromotionDelivery
	require.NoError(t, db.First(&d, "id = ?", delivery.ID).Error)
	require.NotNil(t, d.VisitedAt, "visited_at marcado")
}

func TestVisitPublicBroadcastPromotion_InvalidToken(t *testing.T) {
	db := setupPromoDB(t)
	r := publicPromoRouter(db)

	w := doJSON(t, r, "POST",
		"/api/v1/public/broadcast-promotions/99999999-9999-9999-9999-999999999999/visit", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
