// Spec: specs/070-galeria-multimedia-producto/spec.md
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupMediaDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}, &models.ProductMedia{}))
	return db
}

func mountMediaRouter(db *gorm.DB, tenantID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.TenantIDKey, tenantID); c.Next() })
	r.GET("/products/:id/media", handlers.ListProductMedia(db))
	r.POST("/products/:id/media/youtube", handlers.AddProductMediaYouTube(db))
	r.DELETE("/products/:id/media/:mediaId", handlers.DeleteProductMedia(db, nil))
	return r
}

func seedMediaProduct(t *testing.T, db *gorm.DB, id, tenant string) {
	t.Helper()
	require.NoError(t, db.Create(&models.Product{
		BaseModel: models.BaseModel{ID: id}, TenantID: tenant, Name: "P", Price: 1000,
	}).Error)
}

// Agregar un link de YouTube guarda type youtube + youtube_id + thumbnail.
func TestAddYouTubeMedia(t *testing.T) {
	db := setupMediaDB(t)
	seedMediaProduct(t, db, "p1", "t1")
	r := mountMediaRouter(db, "t1")

	w := doJSON(t, r, http.MethodPost, "/products/p1/media/youtube",
		map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ"})
	require.Equal(t, http.StatusCreated, w.Code)

	var resp struct {
		Data models.ProductMedia `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, models.MediaTypeYouTube, resp.Data.Type)
	require.NotNil(t, resp.Data.YouTubeID)
	assert.Equal(t, "dQw4w9WgXcQ", *resp.Data.YouTubeID)
	assert.Contains(t, resp.Data.URL, "dQw4w9WgXcQ")

	// Link inválido → 400.
	bad := doJSON(t, r, http.MethodPost, "/products/p1/media/youtube",
		map[string]any{"url": "https://vimeo.com/123"})
	assert.Equal(t, http.StatusBadRequest, bad.Code)
}

// El tope MaxMediaPerProduct se aplica en el server.
func TestMediaCap(t *testing.T) {
	db := setupMediaDB(t)
	seedMediaProduct(t, db, "p1", "t1")
	r := mountMediaRouter(db, "t1")
	for i := 0; i < models.MaxMediaPerProduct; i++ {
		w := doJSON(t, r, http.MethodPost, "/products/p1/media/youtube",
			map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ"})
		require.Equal(t, http.StatusCreated, w.Code)
	}
	over := doJSON(t, r, http.MethodPost, "/products/p1/media/youtube",
		map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ"})
	assert.Equal(t, http.StatusBadRequest, over.Code)
}

// Aislamiento por tenant: no se puede tocar media de un producto de otro tenant.
func TestMediaTenantIsolation(t *testing.T) {
	db := setupMediaDB(t)
	seedMediaProduct(t, db, "p1", "tenant-a")
	// El router del tenant-b NO ve el producto del tenant-a.
	rB := mountMediaRouter(db, "tenant-b")
	w := doJSON(t, rB, http.MethodPost, "/products/p1/media/youtube",
		map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ"})
	assert.Equal(t, http.StatusNotFound, w.Code)
}
