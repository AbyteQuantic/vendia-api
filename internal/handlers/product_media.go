// Spec: specs/070-galeria-multimedia-producto/spec.md
package handlers

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const mediaBucket = "product-media"

// ownedProduct verifica que el producto exista y sea del tenant (Art. III).
func ownedProduct(db *gorm.DB, c *gin.Context) (*models.Product, string, bool) {
	tenantID := middleware.GetTenantID(c)
	var product models.Product
	if err := db.Where("id = ? AND tenant_id = ?", c.Param("id"), tenantID).
		First(&product).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
		return nil, "", false
	}
	return &product, tenantID, true
}

// countExtraMedia cuenta las filas de media EXTRA del producto (tope MaxMediaPerProduct).
func countExtraMedia(db *gorm.DB, tenantID, productID string) int64 {
	var n int64
	db.Model(&models.ProductMedia{}).
		Where("tenant_id = ? AND product_id = ?", tenantID, productID).Count(&n)
	return n
}

// ListProductMedia — GET /api/v1/products/:id/media. Filas extra (sin la foto
// principal, que vive en Product) para repintar el editor.
func ListProductMedia(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		_, tenantID, ok := ownedProduct(db, c)
		if !ok {
			return
		}
		var rows []models.ProductMedia
		db.Where("tenant_id = ? AND product_id = ?", tenantID, c.Param("id")).
			Order("position ASC").Find(&rows)
		c.JSON(http.StatusOK, gin.H{"data": rows})
	}
}

// AddProductMediaImage — POST /api/v1/products/:id/media/image. Imagen extra.
func AddProductMediaImage(db *gorm.DB, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		product, tenantID, ok := ownedProduct(db, c)
		if !ok {
			return
		}
		if storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de almacenamiento no configurado"})
			return
		}
		if countExtraMedia(db, tenantID, product.ID) >= models.MaxMediaPerProduct {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("máximo %d elementos por producto", models.MaxMediaPerProduct)})
			return
		}
		file, header, err := c.Request.FormFile("photo")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "imagen requerida (campo: photo)"})
			return
		}
		defer file.Close()
		if header.Size > 5<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "la imagen excede 5MB"})
			return
		}
		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer la imagen"})
			return
		}
		mediaID := uuid.NewString()
		key := fmt.Sprintf("media/%s/%s/%s.webp", tenantID, product.ID, mediaID)
		url, err := storageSvc.Upload(c.Request.Context(), mediaBucket, key, data, header.Header.Get("Content-Type"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al subir la imagen"})
			return
		}
		size := header.Size
		row := models.ProductMedia{
			BaseModel:  models.BaseModel{ID: mediaID},
			TenantID:   tenantID,
			ProductID:  product.ID,
			Type:       models.MediaTypeImage,
			URL:        url,
			Position:   int(countExtraMedia(db, tenantID, product.ID)) + 1,
			StorageKey: &key,
			SizeBytes:  &size,
		}
		if err := db.Create(&row).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar la imagen"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": row})
	}
}

// AddProductMediaVideo — POST /api/v1/products/:id/media/video. Video corto.
// Defensa en profundidad: tamaño <=8MB SIEMPRE + duración (mvhd) <=25s cuando se
// pueda; si no se determina la duración, el peso es el guardrail fail-closed.
func AddProductMediaVideo(db *gorm.DB, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		product, tenantID, ok := ownedProduct(db, c)
		if !ok {
			return
		}
		if storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de almacenamiento no configurado"})
			return
		}
		if countExtraMedia(db, tenantID, product.ID) >= models.MaxMediaPerProduct {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("máximo %d elementos por producto", models.MaxMediaPerProduct)})
			return
		}
		file, header, err := c.Request.FormFile("video")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "video requerido (campo: video)"})
			return
		}
		defer file.Close()
		if header.Size > 8<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el video excede 8MB. Grabe un clip más corto (máx. 25 segundos)."})
			return
		}
		mime := header.Header.Get("Content-Type")
		if !strings.HasPrefix(mime, "video/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el archivo debe ser un video"})
			return
		}
		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer el video"})
			return
		}
		// Autoridad del límite ≤25s: si se determina la duración y supera, rechazar.
		var durPtr *int
		if d, derr := services.Mp4DurationSeconds(data); derr == nil {
			if d > models.MaxVideoDurationS {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("el video dura %ds; el máximo son %d segundos.", d, models.MaxVideoDurationS)})
				return
			}
			durPtr = &d
		}
		mediaID := uuid.NewString()
		key := fmt.Sprintf("media/%s/%s/%s.mp4", tenantID, product.ID, mediaID)
		url, err := storageSvc.Upload(c.Request.Context(), mediaBucket, key, data, mime)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al subir el video"})
			return
		}
		size := header.Size
		row := models.ProductMedia{
			BaseModel:  models.BaseModel{ID: mediaID},
			TenantID:   tenantID,
			ProductID:  product.ID,
			Type:       models.MediaTypeVideo,
			URL:        url,
			Position:   int(countExtraMedia(db, tenantID, product.ID)) + 1,
			DurationS:  durPtr,
			StorageKey: &key,
			SizeBytes:  &size,
		}
		if err := db.Create(&row).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar el video"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": row})
	}
}

// AddProductMediaYouTube — POST /api/v1/products/:id/media/youtube {url}.
// Guarda el link normalizado (no consume R2). Sin fetch externo.
func AddProductMediaYouTube(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		product, tenantID, ok := ownedProduct(db, c)
		if !ok {
			return
		}
		if countExtraMedia(db, tenantID, product.ID) >= models.MaxMediaPerProduct {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("máximo %d elementos por producto", models.MaxMediaPerProduct)})
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "link requerido"})
			return
		}
		id, err := services.ParseYouTubeID(req.URL)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		thumb := services.YouTubeThumbnail(id)
		row := models.ProductMedia{
			BaseModel: models.BaseModel{ID: uuid.NewString()},
			TenantID:  tenantID,
			ProductID: product.ID,
			Type:      models.MediaTypeYouTube,
			URL:       services.YouTubeCanonicalURL(id),
			Thumbnail: &thumb,
			YouTubeID: &id,
			Position:  int(countExtraMedia(db, tenantID, product.ID)) + 1,
		}
		if err := db.Create(&row).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar el video"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": row})
	}
}

// ReorderProductMedia — PATCH /api/v1/products/:id/media/reorder {ids:[...]}.
// Reasigna Position en bloque (1..N) según el orden recibido; solo media del
// tenant y del producto.
func ReorderProductMedia(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		product, tenantID, ok := ownedProduct(db, c)
		if !ok {
			return
		}
		var req struct {
			IDs []string `json:"ids"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "orden inválido"})
			return
		}
		for i, id := range req.IDs {
			db.Model(&models.ProductMedia{}).
				Where("id = ? AND tenant_id = ? AND product_id = ?", id, tenantID, product.ID).
				Update("position", i+1)
		}
		c.JSON(http.StatusOK, gin.H{"message": "orden actualizado"})
	}
}

// DeleteProductMedia — DELETE /api/v1/products/:id/media/:mediaId. Borra el
// objeto R2 (si lo hay) ANTES del soft-delete de la fila.
func DeleteProductMedia(db *gorm.DB, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		product, tenantID, ok := ownedProduct(db, c)
		if !ok {
			return
		}
		var row models.ProductMedia
		if err := db.Where("id = ? AND tenant_id = ? AND product_id = ?",
			c.Param("mediaId"), tenantID, product.ID).First(&row).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "elemento no encontrado"})
			return
		}
		if row.StorageKey != nil && *row.StorageKey != "" && storageSvc != nil {
			_ = storageSvc.Delete(c.Request.Context(), mediaBucket, *row.StorageKey)
		}
		db.Delete(&row)
		c.JSON(http.StatusOK, gin.H{"message": "elemento eliminado"})
	}
}
