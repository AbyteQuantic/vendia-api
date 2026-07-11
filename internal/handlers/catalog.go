package handlers

import (
	"context"
	"net/http"
	"strings"
	"vendia-backend/internal/middleware"
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

// ReferencePhotosByBarcodes — POST /api/v1/catalog/reference-photos
// Spec 097. Body {"barcodes":[...]}. Devuelve un mapa barcode→foto sugerida
// del catálogo para completar productos sin imagen en LOTE. Por cada barcode
// elige la mejor: VERIFICADA (consenso 2+ tiendas) primero; si no hay, el
// respaldo pending (Open Food Facts). Solo filas CON imagen. `verified` le dice
// al frontend cómo marcar la sugerencia. Acota a 200 barcodes por request.
func ReferencePhotosByBarcodes(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Barcodes []string `json:"barcodes"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo inválido"})
			return
		}

		seen := make(map[string]struct{})
		codes := make([]string, 0, len(req.Barcodes))
		for _, b := range req.Barcodes {
			b = strings.TrimSpace(b)
			if b == "" {
				continue
			}
			if _, ok := seen[b]; ok {
				continue
			}
			seen[b] = struct{}{}
			codes = append(codes, b)
			if len(codes) >= 200 {
				break
			}
		}

		out := gin.H{}
		if len(codes) == 0 {
			c.JSON(http.StatusOK, gin.H{"data": out})
			return
		}

		var rows []models.CatalogProduct
		db.Where("barcode IN ? AND image_url <> ''", codes).Find(&rows)

		// Por barcode, verified le gana a pending; si empatan, la primera.
		best := make(map[string]models.CatalogProduct, len(rows))
		for _, row := range rows {
			cur, ok := best[row.Barcode]
			if !ok || (cur.Status != "verified" && row.Status == "verified") {
				best[row.Barcode] = row
			}
		}
		for bc, row := range best {
			out[bc] = gin.H{
				"catalog_product_id": row.ID,
				"image_url":          row.ImageURL,
				"brand":              row.Brand,
				"name":               row.Name,
				"verified":           row.Status == "verified",
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": out})
	}
}

// TakedownCatalogPhoto — POST /api/v1/admin/catalog/takedown (super-admin).
// Spec 098 Adenda A. Proceso de notice-and-takedown: retira una foto del
// catálogo COMPARTIDO (deja de sugerirse a otras tiendas) ante un reclamo de
// derechos. Body: {"barcode": "..."} o {"catalog_image_id": "..."}. NO borra la
// foto propia del tenant (esa es del tendero). La traza (created_by_tenant_id)
// permite identificar quién la aportó.
func TakedownCatalogPhoto(catalogSvc *services.CatalogService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Barcode        string `json:"barcode"`
			CatalogImageID string `json:"catalog_image_id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo inválido"})
			return
		}
		req.Barcode = strings.TrimSpace(req.Barcode)
		req.CatalogImageID = strings.TrimSpace(req.CatalogImageID)

		if req.CatalogImageID != "" {
			if err := catalogSvc.TakedownByImageID(req.CatalogImageID); err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "no se pudo retirar la imagen"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"message": "imagen retirada del catálogo compartido"})
			return
		}
		if req.Barcode != "" {
			n, err := catalogSvc.TakedownByBarcode(req.Barcode)
			if err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "no se encontró el producto de catálogo"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"message": "retirado del catálogo compartido", "removed": n})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "indique barcode o catalog_image_id"})
	}
}

// PhotoVerifier — subconjunto de GeminiService que confirma que una imagen
// corresponde a un producto (Spec 098). Interfaz para poder inyectar un fake
// en tests; en producción la implementa *services.GeminiService.
type PhotoVerifier interface {
	VerifyImageMatchesProduct(ctx context.Context, imageURL, name, presentation string) (bool, error)
}

// ShareProductPhotoToCatalog — POST /api/v1/products/:id/share-to-catalog
// Spec 096 Adenda A. El tendero, tras confirmar explícitamente (el frontend
// SIEMPRE pregunta antes de llamar este endpoint — nunca automático), comparte
// la foto de SU producto para ayudar a otros tenderos con el mismo barcode.
// Requiere que el producto (del propio tenant) tenga barcode + foto.
//
// Spec 103 (B03): esta vía manual exige los MISMOS gates que el aporte
// automático de Spec 098 — ToS vigentes aceptados (la licencia contractual
// sobre la foto) y verificación IA imagen↔producto. Sin ellos, VendIA estaría
// redistribuyendo fotos a otras tiendas sin licencia ni diligencia (Ley
// 23/1982; sin safe harbor en Colombia). Fail-closed: si la IA no puede
// confirmar (Gemini ausente, error o mismatch), NO se aporta.
func ShareProductPhotoToCatalog(db *gorm.DB, catalogSvc *services.CatalogService, verifier PhotoVerifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		productID := c.Param("id")

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ?", productID, tenantID).First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
			return
		}

		if product.Barcode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el producto no tiene código de barras"})
			return
		}
		photo := product.PhotoURL
		if photo == "" {
			photo = product.ImageURL
		}
		if photo == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el producto no tiene foto para compartir"})
			return
		}

		var t models.Tenant
		if err := db.Select("id", "terms_accepted_version").
			First(&t, "id = ?", tenantID).Error; err != nil || !t.AcceptedCurrentTerms() {
			c.JSON(http.StatusForbidden, gin.H{
				"error": "Debe aceptar los Términos y Condiciones vigentes para compartir fotos con otras tiendas.",
				"code":  "terms_required",
			})
			return
		}

		verified := false
		if verifier != nil {
			ok, vErr := verifier.VerifyImageMatchesProduct(c.Request.Context(), photo, product.Name, product.Presentation)
			verified = ok && vErr == nil
		}
		if !verified {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "No pudimos confirmar que la foto corresponda al producto. Intente de nuevo más tarde.",
				"code":  "photo_unverified",
			})
			return
		}

		err := catalogSvc.ShareProductPhotoToCatalog(
			tenantID, product.Barcode, product.Name, "", product.Presentation, product.Content, product.Category, photo,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo compartir la foto"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "foto compartida, gracias por ayudar a otros tenderos"})
	}
}
