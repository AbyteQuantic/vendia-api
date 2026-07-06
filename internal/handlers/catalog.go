package handlers

import (
	"net/http"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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
			SKU          string      `json:"sku,omitempty"` // referencia normalizada (Spec 077/068)
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
				SKU:          r.SKU,
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

// ReferencePhotoByBarcode — GET /api/v1/catalog/reference-photo?barcode=
// Spec 096. Devuelve la foto de catálogo verificada para un código de
// barras EXACTO, o 404 si no hay ninguna (AC-04: sin match, el frontend
// simplemente no muestra la sugerencia — nunca un error visible).
func ReferencePhotoByBarcode(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		barcode := c.Query("barcode")
		if barcode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "barcode requerido"})
			return
		}

		var row models.CatalogProduct
		err := db.Where("barcode = ? AND status = ?", barcode, "verified").First(&row).Error
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "sin foto de referencia para este barcode"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"catalog_product_id": row.ID,
			"image_url":          row.ImageURL,
			"brand":              row.Brand,
			"name":               row.Name,
		}})
	}
}
