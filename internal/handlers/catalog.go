package handlers

import (
	"net/http"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
)

func SearchCatalog(catalogSvc *services.CatalogService, cacheSvc *services.CatalogCacheService) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := c.Query("q")
		if q == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "parámetro q requerido"})
			return
		}

		limit := 10

		// Search: local premium first, OFF fallback if < 3 results
		results, err := catalogSvc.SearchCatalogWithFallback(c.Request.Context(), q, limit, cacheSvc)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"data": []any{}})
			return
		}

		type imageItem struct {
			ID       string `json:"id"`
			ImageURL string `json:"image_url"`
		}

		type resultItem struct {
			ID           string      `json:"id"`
			Name         string      `json:"name"`
			Brand        string      `json:"brand,omitempty"`
			ImageURL     string      `json:"image_url,omitempty"`
			Barcode      string      `json:"barcode,omitempty"`
			Presentation string      `json:"presentation,omitempty"`
			Content      string      `json:"content,omitempty"`
			Source       string      `json:"source"`
			Images       []imageItem `json:"images,omitempty"`
		}

		items := make([]resultItem, 0, len(results))
		for _, r := range results {
			item := resultItem{
				ID:           r.ID,
				Name:         r.Name,
				Brand:        r.Brand,
				ImageURL:     r.ImageURL,
				Barcode:      r.Barcode,
				Presentation: r.Presentation,
				Content:      r.Content,
				Source:       r.Source,
			}
			for _, img := range r.Images {
				item.Images = append(item.Images, imageItem{
					ID:       img.ID,
					ImageURL: img.ImageURL,
				})
			}
			items = append(items, item)
		}

		c.JSON(http.StatusOK, gin.H{"data": items})
	}
}

func GetCatalogImages(catalogSvc *services.CatalogService) gin.HandlerFunc {
	return func(c *gin.Context) {
		catalogID := c.Param("id")

		images, err := catalogSvc.GetAcceptedImages(catalogID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener imágenes"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": images})
	}
}

func AcceptCatalogImage(catalogSvc *services.CatalogService) gin.HandlerFunc {
	return func(c *gin.Context) {
		imageID := c.Param("image_id")

		if err := catalogSvc.AcceptImage(imageID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al aceptar imagen"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "imagen aceptada"})
	}
}
