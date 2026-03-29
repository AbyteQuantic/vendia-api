package handlers

import (
	"context"
	"net/http"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func SuggestSong(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		TrackName   string `json:"track_name"  binding:"required"`
		ArtistName  string `json:"artist_name" binding:"required"`
		ArtworkURL  string `json:"artwork_url"`
		SuggestedBy string `json:"suggested_by"`
	}

	return func(c *gin.Context) {
		slug := c.Param("slug")

		var tenant models.Tenant
		if err := db.Where("store_slug = ?", slug).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "tienda no encontrada"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		suggestion := models.RockolaSuggestion{
			TenantID:    tenant.ID,
			TrackName:   req.TrackName,
			ArtistName:  req.ArtistName,
			ArtworkURL:  req.ArtworkURL,
			Status:      "pending",
			SuggestedBy: req.SuggestedBy,
		}

		if err := db.Create(&suggestion).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al sugerir canción"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": suggestion})
	}
}

func PendingSongs(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var songs []models.RockolaSuggestion
		if err := db.Where("tenant_id = ? AND status = 'pending'", tenantID).
			Order("created_at ASC").
			Find(&songs).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener canciones"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": songs})
	}
}

func MarkSongPlayed(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		uuid := c.Param("uuid")

		result := db.Model(&models.RockolaSuggestion{}).
			Where("id = ? AND tenant_id = ?", uuid, tenantID).
			Update("status", "played")

		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "canción no encontrada"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "canción marcada como sonada"})
	}
}

func SearchSongs(itunesSvc *services.ITunesService) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Query("q")
		if query == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "parámetro q requerido"})
			return
		}

		if itunesSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de música no disponible"})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()

		tracks, err := itunesSvc.Search(ctx, query, 5)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al buscar canciones"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": tracks})
	}
}
