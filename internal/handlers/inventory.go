package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"vendia-backend/internal/aiusage"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// aiImageOperationTimeout is the total budget for an AI photo
// operation (enhance or generate): download the source photo +
// Gemini image call (~27s measured in production) + R2 upload.
//
// Spec: specs/015-ia-foto-timeouts/spec.md — FR-01 / D1.
// It must stay BELOW the frontend per-request receiveTimeout (~140s)
// so the backend always responds — with a clear Spanish error if the
// context is exhausted — before the client cuts the connection.
const aiImageOperationTimeout = 110 * time.Second

// aiTimeoutMessage is the Spanish, user-facing error returned when an
// AI photo operation runs past its context budget.
//
// Spec: specs/015-ia-foto-timeouts/spec.md — FR-03 / Constitution Art. V.
const aiTimeoutMessage = "La IA está tardando más de lo normal. Intenta de nuevo en un momento."

// respondAIImageError maps an error from an AI photo operation to a
// clean HTTP response. When the failure is a context deadline /
// cancellation, it fails FAST with a clear Spanish message and 504 —
// the handler never hangs past its context and never leaks the raw
// "context deadline exceeded" string to the shopkeeper. Any other
// error keeps the supplied Spanish prefix.
//
// Spec: specs/015-ia-foto-timeouts/spec.md — FR-03.
func respondAIImageError(c *gin.Context, ctx context.Context, prefix string, err error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		ctx.Err() != nil {
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": aiTimeoutMessage})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("%s: %v", prefix, err)})
}

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

		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), tenantID),
			30*time.Second,
		)
		defer cancel()

		result, err := geminiSvc.ScanInvoice(ctx, data, mimeType)
		if err != nil {
			// Return 422 for AI/parsing errors (not a server crash)
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":  "No se pudieron leer los productos de la factura. Intente tomar la foto con mejor iluminación.",
				"detail": err.Error(),
			})
			return
		}

		if len(result.Products) == 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "No se encontraron productos legibles en la factura. Intente con mejor iluminación o más cerca del texto.",
			})
			return
		}

		type ProductResult struct {
			Name           string  `json:"name"`
			Presentation   string  `json:"presentation,omitempty"`
			Content        string  `json:"content,omitempty"`
			Quantity       int     `json:"quantity"`
			UnitPrice      float64 `json:"unit_price"`
			TotalPrice     float64 `json:"total_price"`
			Barcode        string  `json:"barcode,omitempty"`
			ImageURL       string  `json:"image_url,omitempty"`
			ExpiryDate     string  `json:"expiry_date,omitempty"`
			Confidence     float64 `json:"confidence"`
			Status         string  `json:"status"`
			MatchProductID string  `json:"match_product_id,omitempty"`
			MatchMethod    string  `json:"match_method,omitempty"`
			SupplierID     string  `json:"supplier_id,omitempty"`
		}

		// Auto-link supplier: if Gemini detected a provider name, try to
		// match it against existing suppliers so new products get supplier_id.
		var matchedSupplierID *string
		if result.Provider != "" && result.Provider != "Desconocido" {
			normProvider := services.NormalizeText(result.Provider)
			var suppliers []models.Supplier
			db.Where("tenant_id = ?", tenantID).Find(&suppliers)
			for _, s := range suppliers {
				if services.NormalizeText(s.CompanyName) == normProvider {
					matchedSupplierID = &s.ID
					break
				}
			}
		}

		// Resolve branch scope for dedup queries — products in branch A
		// must not match products in branch B.
		branchID := middleware.GetBranchID(c)

		var products []ProductResult
		for _, p := range result.Products {
			// Validate the expiry_date that Gemini extracted. Bad formats
			// are dropped so they never reach the DB; the shopkeeper can
			// add or correct the date later on the review screen.
			expiryForDB, _ := normaliseExpiryDate(p.ExpiryDate)
			expiryForResponse := ""
			if expiryForDB != nil {
				expiryForResponse = *expiryForDB
			}

			// Append content to name so references don't look like duplicates
			// e.g. "Coca Cola" + "1.5L" → "Coca Cola 1.5L"
			displayName := p.Name
			if p.Content != "" {
				displayName += " " + p.Content
			}

			pr := ProductResult{
				Name:         displayName,
				Presentation: p.Presentation,
				Content:      p.Content,
				Quantity:     p.Quantity,
				UnitPrice:    p.UnitPrice,
				TotalPrice:   p.TotalPrice,
				Barcode:      p.Barcode,
				ExpiryDate:   expiryForResponse,
				Confidence:   p.Confidence,
				Status:       "precio_pendiente",
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

			// 3-level dedup: return match info without modifying DB
			var existing models.Product
			matched := false
			matchMethod := ""

			// Level 1: barcode exact (branch-scoped)
			if pr.Barcode != "" {
				barcodeQ := db.Where("barcode = ? AND tenant_id = ?", pr.Barcode, tenantID)
				if branchID != "" {
					barcodeQ = barcodeQ.Where("branch_id = ?", branchID)
				}
				if err := barcodeQ.First(&existing).Error; err == nil {
					matched = true
					matchMethod = "barcode"
				}
			}

			// Level 2: normalized name+presentation+content (branch-scoped)
			if !matched {
				normKey := services.NormalizeText(displayName) + "|" +
					services.NormalizeText(pr.Presentation) + "|" +
					services.NormalizeText(pr.Content)
				var candidates []models.Product
				candQ := db.Where("tenant_id = ? AND is_available = true", tenantID)
				if branchID != "" {
					candQ = candQ.Where("branch_id = ?", branchID)
				}
				candQ.Find(&candidates)
				for _, cand := range candidates {
					cKey := services.NormalizeText(cand.Name) + "|" +
						services.NormalizeText(cand.Presentation) + "|" +
						services.NormalizeText(cand.Content)
					if cKey == normKey {
						existing = cand
						matched = true
						matchMethod = "normalized"
						break
					}
				}
			}

			// Level 3: pg_trgm fuzzy (branch-scoped)
			if !matched && displayName != "" {
				normName := services.NormalizeText(displayName)
				var fuzzy struct {
					models.Product
					Similarity float64
				}
				fuzzySQL := `
					SELECT p.*, similarity(LOWER(p.name), ?) AS similarity
					FROM products p
					WHERE p.tenant_id = ? AND p.is_available = true
					  AND p.deleted_at IS NULL
					  AND similarity(LOWER(p.name), ?) > 0.6`
				fuzzyArgs := []any{normName, tenantID, normName}
				if branchID != "" {
					fuzzySQL += ` AND p.branch_id = ?`
					fuzzyArgs = append(fuzzyArgs, branchID)
				}
				fuzzySQL += ` ORDER BY similarity DESC LIMIT 1`
				if err := db.Raw(fuzzySQL, fuzzyArgs...).Scan(&fuzzy).Error; err == nil && fuzzy.Product.ID != "" {
					existing = fuzzy.Product
					matched = true
					matchMethod = "fuzzy"
				}
			}

			if matched {
				pr.Status = "match_encontrado"
				pr.MatchProductID = existing.ID
				pr.MatchMethod = matchMethod
			} else {
				pr.Status = "nuevo"
			}
			if matchedSupplierID != nil {
				pr.SupplierID = *matchedSupplierID
			}
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

// CatalogDump returns all cached catalog products for offline-first sync.
func CatalogDump(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var products []models.CatalogProduct
		if err := db.Order("name ASC").Limit(500).Find(&products).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener catálogo"})
			return
		}

		type item struct {
			Name     string `json:"name"`
			Brand    string `json:"brand"`
			ImageURL string `json:"image_url"`
		}

		items := make([]item, 0, len(products))
		for _, p := range products {
			items = append(items, item{Name: p.Name, Brand: p.Brand, ImageURL: p.ImageURL})
		}

		c.JSON(http.StatusOK, gin.H{"data": items})
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

func UploadProductPhoto(db *gorm.DB, storageSvc services.FileStorage) gin.HandlerFunc {
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

		if storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de almacenamiento no configurado"})
			return
		}

		key := fmt.Sprintf("products/%s/%s.webp", tenantID, productUUID)
		mimeType := header.Header.Get("Content-Type")

		photoURL, err := storageSvc.Upload(c.Request.Context(), "product-photos", key, data, mimeType)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al subir foto"})
			return
		}

		db.Model(&product).Update("photo_url", photoURL)

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"photo_url": photoURL}})
	}
}

func EnhanceProductPhoto(db *gorm.DB, geminiSvc *services.GeminiService, storageSvc services.FileStorage, catalogSvc *services.CatalogService) gin.HandlerFunc {
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

		if geminiSvc == nil || storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicios de IA no configurados"})
			return
		}

		sourceURL := product.PhotoURL
		if sourceURL == "" {
			sourceURL = product.ImageURL
		}

		// Spec: specs/015-ia-foto-timeouts/spec.md — FR-01.
		// A single Gemini image operation measured ~27s in production;
		// add the source-photo download and the R2 upload on top and
		// 30s is not enough. 110s covers the whole operation with
		// headroom, and stays below the frontend's ~140s receiveTimeout
		// so the backend always answers before the client gives up.
		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), tenantID),
			aiImageOperationTimeout,
		)
		defer cancel()

		imgReq, err := http.NewRequestWithContext(ctx, "GET", sourceURL, nil)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al preparar descarga de foto"})
			return
		}
		imgReq.Header.Set("User-Agent", "VendIA-POS/1.0 (vendia.co)")

		resp, err := http.DefaultClient.Do(imgReq)
		if err != nil {
			respondAIImageError(c, ctx, "error al obtener foto", err)
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

		// Use fresh context from query params if provided (edit screen sends current UI state)
		name := c.Query("name")
		if name == "" {
			name = product.Name
		}
		pres := c.Query("presentation")
		if pres == "" {
			pres = product.Presentation
		}
		content := c.Query("content")
		if content == "" {
			content = product.Content
		}
		productInfo := name
		if pres != "" {
			productInfo += " " + pres
		}
		if content != "" {
			productInfo += " " + content
		}

		enhanced, err := geminiSvc.EnhancePhoto(ctx, imageData, contentType, productInfo)
		if err != nil {
			respondAIImageError(c, ctx, "error al mejorar foto", err)
			return
		}

		key := fmt.Sprintf("products/%s/%s-enhanced.png", tenantID, productUUID)
		newURL, err := storageSvc.Upload(ctx, "product-photos", key, enhanced, "image/png")
		if err != nil {
			respondAIImageError(c, ctx, "error al guardar foto mejorada", err)
			return
		}

		db.Model(&product).Updates(map[string]any{"photo_url": newURL, "is_ai_enhanced": true})

		// Catalog integration
		var catalogImageID string
		if catalogSvc != nil {
			key := fmt.Sprintf("products/%s/%s-enhanced.png", tenantID, productUUID)
			cp, err := catalogSvc.FindOrCreateCatalogProduct(
				product.Barcode, product.Name, "", product.Presentation, product.Content, "")
			if err == nil {
				count, _ := catalogSvc.CountAcceptedImages(cp.ID)
				if count < 3 {
					img, err := catalogSvc.CreatePendingImage(cp.ID, tenantID, newURL, key)
					if err == nil {
						catalogImageID = img.ID
					}
				}
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"photo_url":        newURL,
			"catalog_image_id": catalogImageID,
		}})
	}
}

func GenerateProductImage(db *gorm.DB, geminiSvc *services.GeminiService, storageSvc services.FileStorage, catalogSvc *services.CatalogService) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		productUUID := c.Param("id")

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ?", productUUID, tenantID).
			First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
			return
		}

		if geminiSvc == nil || storageSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicios de IA no configurados"})
			return
		}

		// Use fresh context from query params if provided (edit screen sends current UI state)
		name := c.Query("name")
		if name == "" {
			name = product.Name
		}
		pres := c.Query("presentation")
		if pres == "" {
			pres = product.Presentation
		}
		content := c.Query("content")
		if content == "" {
			content = product.Content
		}
		barcode := c.Query("barcode")
		if barcode == "" {
			barcode = product.Barcode
		}
		productInfo := name
		if pres != "" {
			productInfo += " " + pres
		}
		if content != "" {
			productInfo += " " + content
		}
		if barcode != "" {
			productInfo += " (codigo de barras: " + barcode + ")"
		}

		// Spec: specs/015-ia-foto-timeouts/spec.md — FR-01.
		// Aligned with EnhanceProductPhoto: a Gemini image operation
		// (~27s) plus the R2 upload needs a generous, ordered budget.
		ctx, cancel := context.WithTimeout(
			aiusage.WithTenantID(c.Request.Context(), tenantID),
			aiImageOperationTimeout,
		)
		defer cancel()

		generated, err := geminiSvc.GenerateProductImage(ctx, productInfo)
		if err != nil {
			respondAIImageError(c, ctx, "error al generar imagen", err)
			return
		}

		key := fmt.Sprintf("products/%s/%s-generated.png", tenantID, productUUID)
		newURL, err := storageSvc.Upload(ctx, "product-photos", key, generated, "image/png")
		if err != nil {
			respondAIImageError(c, ctx, "error al guardar imagen", err)
			return
		}

		db.Model(&product).Updates(map[string]any{"photo_url": newURL, "image_url": newURL, "is_ai_enhanced": true})

		// Catalog integration
		var catalogImageID string
		if catalogSvc != nil {
			cp, err := catalogSvc.FindOrCreateCatalogProduct(
				product.Barcode, product.Name, "", product.Presentation, product.Content, "")
			if err == nil {
				count, _ := catalogSvc.CountAcceptedImages(cp.ID)
				if count < 3 {
					img, err := catalogSvc.CreatePendingImage(cp.ID, tenantID, newURL, key)
					if err == nil {
						catalogImageID = img.ID
					}
				}
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"photo_url":        newURL,
			"catalog_image_id": catalogImageID,
		}})
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
		scope := ResolveBranchScope(c, db)

		var products []models.Product
		q := db.Where("tenant_id = ? AND is_available = true AND stock <= min_stock AND min_stock > 0", tenantID)
		q = ApplyBranchScope(q, scope)
		if err := q.Order("stock ASC").Find(&products).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener alertas"})
			return
		}

		// Collect supplier IDs to batch-load names
		supplierIDs := map[string]bool{}
		for _, p := range products {
			if p.SupplierID != nil && *p.SupplierID != "" {
				supplierIDs[*p.SupplierID] = true
			}
		}
		supplierMap := map[string]models.Supplier{}
		if len(supplierIDs) > 0 {
			ids := make([]string, 0, len(supplierIDs))
			for id := range supplierIDs {
				ids = append(ids, id)
			}
			var suppliers []models.Supplier
			db.Where("id IN ?", ids).Find(&suppliers)
			for _, s := range suppliers {
				supplierMap[s.ID] = s
			}
		}

		type AlertItem struct {
			models.Product
			SupplierName  string `json:"supplier_name,omitempty"`
			SupplierPhone string `json:"supplier_phone,omitempty"`
			SupplierEmoji string `json:"supplier_emoji,omitempty"`
		}

		items := make([]AlertItem, 0, len(products))
		for _, p := range products {
			item := AlertItem{Product: p}
			if p.SupplierID != nil {
				if s, ok := supplierMap[*p.SupplierID]; ok {
					item.SupplierName = s.CompanyName
					item.SupplierPhone = s.Phone
					item.SupplierEmoji = s.Emoji
				}
			}
			items = append(items, item)
		}

		c.JSON(http.StatusOK, gin.H{"data": items})
	}
}

// ReorderSuggestions groups low-stock products by supplier for easy
// one-tap ordering. Products without a supplier go into an "unlinked" group.
// GET /api/v1/inventory/reorder-suggestions
func ReorderSuggestions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)

		var products []models.Product
		q := db.Where("tenant_id = ? AND is_available = true AND stock <= min_stock AND min_stock > 0", tenantID)
		q = ApplyBranchScope(q, scope)
		if err := q.Order("stock ASC").Find(&products).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener sugerencias"})
			return
		}

		// Load all suppliers for this tenant
		var suppliers []models.Supplier
		db.Where("tenant_id = ?", tenantID).Find(&suppliers)
		supplierMap := map[string]models.Supplier{}
		for _, s := range suppliers {
			supplierMap[s.ID] = s
		}

		type ProductLine struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Stock        int    `json:"stock"`
			MinStock     int    `json:"min_stock"`
			SuggestOrder int    `json:"suggest_order"` // min_stock - stock (how many to reorder)
		}
		type SupplierGroup struct {
			SupplierID    string        `json:"supplier_id"`
			SupplierName  string        `json:"supplier_name"`
			SupplierPhone string        `json:"supplier_phone"`
			SupplierEmoji string        `json:"supplier_emoji"`
			Products      []ProductLine `json:"products"`
			TotalItems    int           `json:"total_items"`
		}

		groups := map[string]*SupplierGroup{}
		unlinked := &SupplierGroup{
			SupplierName: "Sin proveedor asignado",
		}

		for _, p := range products {
			line := ProductLine{
				ID:           p.ID,
				Name:         p.Name,
				Stock:        p.Stock,
				MinStock:     p.MinStock,
				SuggestOrder: p.MinStock - p.Stock,
			}
			if line.SuggestOrder < 1 {
				line.SuggestOrder = 1
			}

			sid := ""
			if p.SupplierID != nil {
				sid = *p.SupplierID
			}
			if sid != "" {
				if _, ok := groups[sid]; !ok {
					s := supplierMap[sid]
					groups[sid] = &SupplierGroup{
						SupplierID:    s.ID,
						SupplierName:  s.CompanyName,
						SupplierPhone: s.Phone,
						SupplierEmoji: s.Emoji,
					}
				}
				groups[sid].Products = append(groups[sid].Products, line)
				groups[sid].TotalItems += line.SuggestOrder
			} else {
				unlinked.Products = append(unlinked.Products, line)
				unlinked.TotalItems += line.SuggestOrder
			}
		}

		result := make([]SupplierGroup, 0, len(groups)+1)
		for _, g := range groups {
			result = append(result, *g)
		}
		if len(unlinked.Products) > 0 {
			result = append(result, *unlinked)
		}

		c.JSON(http.StatusOK, gin.H{"data": result})
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
