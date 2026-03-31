package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func ScanInvoice(db *gorm.DB, geminiSvc *services.GeminiService, offSvc *services.OpenFoodFactsService) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		if geminiSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de IA no configurado"})
			return
		}

		file, header, err := c.Request.FormFile("image")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "imagen requerida (campo: image)"})
			return
		}
		defer file.Close()

		if header.Size > 5<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "imagen excede 5MB"})
			return
		}

		mimeType := header.Header.Get("Content-Type")
		if mimeType != "image/jpeg" && mimeType != "image/png" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "solo se aceptan JPEG y PNG"})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer imagen"})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()

		result, err := geminiSvc.ScanInvoice(ctx, data, mimeType)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("error al procesar factura: %v", err)})
			return
		}

		type ProductResult struct {
			Name       string  `json:"name"`
			Quantity   int     `json:"quantity"`
			UnitPrice  float64 `json:"unit_price"`
			TotalPrice float64 `json:"total_price"`
			Barcode    string  `json:"barcode,omitempty"`
			ImageURL   string  `json:"image_url,omitempty"`
			Confidence float64 `json:"confidence"`
			Status     string  `json:"status"`
		}

		var products []ProductResult
		for _, p := range result.Products {
			pr := ProductResult{
				Name:       p.Name,
				Quantity:   p.Quantity,
				UnitPrice:  p.UnitPrice,
				TotalPrice: p.TotalPrice,
				Barcode:    p.Barcode,
				Confidence: p.Confidence,
				Status:     "precio_pendiente",
			}

			if p.Barcode != "" && offSvc != nil {
				offProduct, err := offSvc.LookupBarcode(ctx, p.Barcode)
				if err == nil && offProduct != nil {
					pr.ImageURL = offProduct.ImageURL
					if pr.Name == "" && offProduct.Name != "" {
						pr.Name = offProduct.Name
					}
				}
			}

			product := models.Product{
				BaseModel:       models.BaseModel{ID: uuid.NewString()},
				TenantID:        tenantID,
				Name:            pr.Name,
				PurchasePrice:   pr.UnitPrice,
				Stock:           pr.Quantity,
				Barcode:         pr.Barcode,
				ImageURL:        pr.ImageURL,
				IsAvailable:     true,
				IngestionMethod: "ia_factura",
				PriceStatus:     "pending",
			}

			var existing models.Product
			if pr.Barcode != "" {
				if err := db.Where("barcode = ? AND tenant_id = ?", pr.Barcode, tenantID).
					First(&existing).Error; err == nil {
					db.Model(&existing).Updates(map[string]any{
						"stock":          gorm.Expr("stock + ?", pr.Quantity),
						"purchase_price": pr.UnitPrice,
					})
					pr.Status = "actualizado"
					products = append(products, pr)
					continue
				}
			}

			db.Create(&product)
			products = append(products, pr)
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"provider":      result.Provider,
				"products":      products,
				"invoice_total": result.InvoiceTotal,
			},
		})
	}
}

func SearchProductsOFF(cacheSvc *services.CatalogCacheService) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := c.Query("q")
		if q == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "parámetro q requerido"})
			return
		}

		if cacheSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio no disponible"})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
		defer cancel()

		products, err := cacheSvc.SearchProducts(ctx, q, 5)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"data": []any{}})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": products})
	}
}

func LookupBarcode(offSvc *services.OpenFoodFactsService) gin.HandlerFunc {
	return func(c *gin.Context) {
		barcode := c.Query("barcode")
		if barcode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "barcode requerido"})
			return
		}

		if offSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio no disponible"})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()

		product, err := offSvc.LookupBarcode(ctx, barcode)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al buscar producto"})
			return
		}

		if product == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado en Open Food Facts"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": product})
	}
}

func UploadProductPhoto(db *gorm.DB, r2Svc *services.R2Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		productUUID := c.Param("id")

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ?", productUUID, tenantID).
			First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
			return
		}

		file, header, err := c.Request.FormFile("photo")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "foto requerida (campo: photo)"})
			return
		}
		defer file.Close()

		if header.Size > 5<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "foto excede 5MB"})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer foto"})
			return
		}

		if r2Svc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de almacenamiento no configurado"})
			return
		}

		key := fmt.Sprintf("products/%s/%s.webp", tenantID, productUUID)
		mimeType := header.Header.Get("Content-Type")

		photoURL, err := r2Svc.Upload(c.Request.Context(), "vendia-product-photos", key, data, mimeType)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al subir foto"})
			return
		}

		db.Model(&product).Update("photo_url", photoURL)

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"photo_url": photoURL}})
	}
}

func EnhanceProductPhoto(db *gorm.DB, geminiSvc *services.GeminiService, r2Svc *services.R2Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		productUUID := c.Param("id")

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ?", productUUID, tenantID).
			First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
			return
		}

		if product.PhotoURL == "" && product.ImageURL == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el producto no tiene foto para mejorar"})
			return
		}

		if geminiSvc == nil || r2Svc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicios de IA no configurados"})
			return
		}

		sourceURL := product.PhotoURL
		if sourceURL == "" {
			sourceURL = product.ImageURL
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()

		imgReq, err := http.NewRequestWithContext(ctx, "GET", sourceURL, nil)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al preparar descarga de foto"})
			return
		}
		imgReq.Header.Set("User-Agent", "VendIA-POS/1.0 (vendia.co)")

		resp, err := http.DefaultClient.Do(imgReq)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("error al obtener foto: %v", err)})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("la URL de la foto devolvió %d", resp.StatusCode)})
			return
		}

		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(contentType, "image/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "la URL no contiene una imagen válida"})
			return
		}

		imageData, err := io.ReadAll(resp.Body)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer foto"})
			return
		}

		// Build product description for better AI results
		productInfo := product.Name
		if product.Presentation != "" {
			productInfo += " " + product.Presentation
		}
		if product.Content != "" {
			productInfo += " " + product.Content
		}

		enhanced, err := geminiSvc.EnhancePhoto(ctx, imageData, contentType, productInfo)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("error al mejorar foto: %v", err)})
			return
		}

		key := fmt.Sprintf("products/%s/%s-enhanced.webp", tenantID, productUUID)
		newURL, err := r2Svc.Upload(ctx, "vendia-product-photos", key, enhanced, "image/webp")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al guardar foto mejorada"})
			return
		}

		db.Model(&product).Update("photo_url", newURL)

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"photo_url": newURL}})
	}
}

func PendingPrices(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		p := parsePagination(c)

		var total int64
		query := db.Model(&models.Product{}).Where("tenant_id = ? AND price_status = 'pending'", tenantID)
		query.Count(&total)

		var products []models.Product
		if err := query.Order("created_at DESC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&products).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener productos"})
			return
		}

		type PendingProduct struct {
			models.Product
			SuggestedPrice float64 `json:"suggested_price"`
			Profit         float64 `json:"profit"`
		}

		var result []PendingProduct
		for _, prod := range products {
			suggested := services.SuggestPrice(prod.PurchasePrice, 30)
			result = append(result, PendingProduct{
				Product:        prod,
				SuggestedPrice: suggested,
				Profit:         services.CalculateProfit(suggested, prod.PurchasePrice),
			})
		}

		c.JSON(http.StatusOK, newPaginatedResponse(result, total, p))
	}
}

func SetProductPrice(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Price float64 `json:"price" binding:"required,gt=0"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		productUUID := c.Param("id")

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ?", productUUID, tenantID).
			First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		rounded := services.RoundCOP(req.Price)

		db.Model(&product).Updates(map[string]any{
			"price":        rounded,
			"price_status": "set",
		})

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"product_uuid": product.ID,
				"price":        rounded,
				"profit":       services.CalculateProfit(rounded, product.PurchasePrice),
			},
		})
	}
}

func InventoryAlerts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var products []models.Product
		if err := db.Where("tenant_id = ? AND is_available = true AND stock <= min_stock AND min_stock > 0", tenantID).
			Order("stock ASC").
			Find(&products).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener alertas"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": products})
	}
}

func ExpiringProducts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		sevenDaysFromNow := time.Now().AddDate(0, 0, 7).Format("2006-01-02")

		var products []models.Product
		if err := db.Where("tenant_id = ? AND is_available = true AND expiry_date IS NOT NULL AND expiry_date <= ? AND expiry_date >= ?",
			tenantID, sevenDaysFromNow, time.Now().Format("2006-01-02")).
			Order("expiry_date ASC").
			Find(&products).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener productos por vencer"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": products})
	}
}
