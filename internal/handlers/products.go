package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// resolveDefaultBranchID returns the tenant's "sede por defecto" — the
// oldest non-deleted branch (Spec 014 §6). Used by CreateProduct as a
// fallback when the JWT carries no branch_id claim (a mono-sede owner's
// token), so a product is never inserted with branch_id NULL (FR-02).
//
// Returns an empty string when the tenant has no branch at all; the
// caller then keeps the current behaviour (branch_id NULL) because there
// is no sede to assign. The tie-breaker (`created_at ASC, id ASC`)
// matches database.BackfillBranchIDs so the live fallback and the
// historical backfill agree on the same default sede.
func resolveDefaultBranchID(db *gorm.DB, tenantID string) string {
	if tenantID == "" {
		return ""
	}
	var branch models.Branch
	err := db.Select("id").
		Where("tenant_id = ?", tenantID).
		Order("created_at ASC, id ASC").
		First(&branch).Error
	if err != nil {
		// gorm.ErrRecordNotFound (tenant with no sede) or a real DB
		// error — either way we have no sede to assign. Fall through
		// to NULL; the boot-time backfill is the safety net.
		return ""
	}
	return branch.ID
}

// normaliseExpiryDate validates an incoming expiry date string. Accepts
// ISO-8601 dates ("2026-12-31") only. Empty or whitespace maps to nil
// (no expiration). Any other input is rejected so the Postgres DATE
// column never receives garbage.
func normaliseExpiryDate(raw string) (*string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if _, err := time.Parse("2006-01-02", trimmed); err != nil {
		return nil, fmt.Errorf("expiry_date debe tener formato YYYY-MM-DD")
	}
	return &trimmed, nil
}

func ListProducts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		p := parsePagination(c)

		// Phase-6 branch isolation: when the caller passes
		// ?branch_id= (or carries one in the JWT workspace claim),
		// the inventory listing filters to that sede only. Callers
		// without any branch context get the legacy global view —
		// mono-sede tenants keep working unchanged.
		scope := ResolveBranchScope(c, db)
		if scope.NotOwned {
			c.JSON(http.StatusForbidden, gin.H{
				"error":      "la sucursal no pertenece al negocio",
				"error_code": "branch_not_owned",
			})
			return
		}

		query := db.Model(&models.Product{}).
			Where("tenant_id = ? AND is_available = true", tenantID)
		query = ApplyBranchScope(query, scope)

		var total int64
		query.Count(&total)

		var products []models.Product
		if err := query.
			Order("name ASC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&products).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener productos"})
			return
		}

		c.JSON(http.StatusOK, newPaginatedResponse(products, total, p))
	}
}

// LookupProductByBarcode searches across the ENTIRE tenant catalog
// (no branch filter) for a product matching the given barcode. Used
// by the barcode scanner in the POS — a cashier in branch A should
// still find products that live in branch B so they can be added to
// the cart or associated.
func LookupProductByBarcode(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		if tenantID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "token sin tenant"})
			return
		}
		code := c.Query("code")
		if code == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "parámetro 'code' requerido"})
			return
		}
		var product models.Product
		err := db.Where("tenant_id = ? AND barcode = ?", tenantID, code).First(&product).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado con ese código"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al buscar producto"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": product})
	}
}

func CreateProduct(db *gorm.DB, catalogSvc *services.CatalogService) gin.HandlerFunc {
	type Request struct {
		ID                string  `json:"id"`
		Name              string  `json:"name"     binding:"required"`
		Price             float64 `json:"price"    binding:"required,gt=0"`
		Stock             int     `json:"stock"`
		Barcode           string  `json:"barcode"`
		ImageURL          string  `json:"image_url"`
		RequiresContainer bool    `json:"requires_container"`
		ContainerPrice    int64   `json:"container_price"`
		CatalogImageID    string  `json:"catalog_image_id"`
		Presentation      string  `json:"presentation"`
		Content           string  `json:"content"`
		ExpiryDate        string  `json:"expiry_date"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		branchID := middleware.GetBranchID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id must be a valid UUID v4"})
			return
		}

		// Feature 014 / FR-05 — idempotent re-sync. The product UUID is
		// generated client-side (Art. II offline-first); an offline POS
		// that re-syncs a product it already persisted would otherwise hit
		// the products_pkey UNIQUE constraint on db.Create below and the
		// handler would leak a raw Postgres `duplicate key` error as an
		// HTTP 500 — English, ugly, a dead end for the tendero, and it
		// also swallowed by create_product_screen.dart's mute catchError.
		//
		// Instead: if a client provided an `id` that already belongs to a
		// live (non-soft-deleted) product for THIS tenant, the creation
		// already succeeded. Return that existing product with HTTP 200
		// and stop. The first write wins — we never overwrite it with the
		// new payload, and because this return happens BEFORE db.Create,
		// the initial_stock kardex movement is never logged twice (AC-03).
		// The query is tenant-scoped (Art. III) and GORM's default
		// soft-delete scope keeps it to live products only. This mirrors
		// the CreateSale idempotency pattern in sales.go exactly.
		if req.ID != "" {
			var existing models.Product
			lookupErr := db.Where("id = ? AND tenant_id = ?", req.ID, tenantID).
				First(&existing).Error
			if lookupErr == nil {
				c.JSON(http.StatusOK, gin.H{"data": existing})
				return
			}
			if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
				// A real DB error (not a clean miss) — surface it in
				// Spanish instead of falling through to a create that
				// would also fail and leak the raw driver message.
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "no se pudo verificar el producto",
				})
				return
			}
			// Clean miss → fresh product, fall through to normal creation.
		}

		// Feature 014 / FR-02 — sede fallback. A mono-sede owner's JWT
		// carries no branch_id claim, so GetBranchID returns "" →
		// UUIDPtr("") → nil → the product would be inserted with
		// branch_id NULL and vanish from sede-scoped Inventario/Dashboard
		// reads (the exact bug this feature fixes). When the claim is
		// empty, resolve the tenant's default sede and scope the product
		// to it. If the tenant has no sede at all, branchID stays empty
		// and the current behaviour (branch_id NULL) is preserved — the
		// boot-time backfill is the safety net.
		if branchID == "" {
			branchID = resolveDefaultBranchID(db, tenantID)
		}

		expiry, err := normaliseExpiryDate(req.ExpiryDate)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		product := models.Product{
			TenantID:          tenantID,
			CreatedBy:         middleware.UUIDPtr(userID),
			BranchID:          middleware.UUIDPtr(branchID),
			Name:              req.Name,
			Price:             req.Price,
			Stock:             req.Stock,
			Barcode:           req.Barcode,
			ImageURL:          req.ImageURL,
			IsAvailable:       true,
			RequiresContainer: req.RequiresContainer,
			ContainerPrice:    req.ContainerPrice,
			Presentation:      req.Presentation,
			Content:           req.Content,
			ExpiryDate:        expiry,
		}
		if req.ID != "" {
			product.ID = req.ID
		}

		if err := db.Create(&product).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear producto"})
			return
		}

		if product.Stock > 0 {
			// FR-03 — the product row already carries stock_inicial, so
			// LogInventoryMovement's self-read would record
			// stock_before=stock_inicial / stock_after=2×stock_inicial.
			// An initial_stock movement always goes from 0 to the full
			// starting quantity: pass that snapshot explicitly.
			zero := float64(0)
			initial := float64(product.Stock)
			services.LogInventoryMovement(db, services.MovementParams{
				TenantID:            tenantID,
				BranchID:            middleware.UUIDPtr(branchID),
				ProductID:           product.ID,
				ProductName:         product.Name,
				MovementType:        models.MovementInitialStock,
				Quantity:            product.Stock,
				UserID:              middleware.UUIDPtr(userID),
				StockBeforeOverride: &zero,
				StockAfterOverride:  &initial,
			})
		}

		// Accept catalog image if provided
		if req.CatalogImageID != "" && catalogSvc != nil {
			catalogSvc.AcceptImage(req.CatalogImageID)
		}

		c.JSON(http.StatusCreated, gin.H{"data": product})
	}
}

func UpdateProduct(db *gorm.DB, catalogSvc *services.CatalogService) gin.HandlerFunc {
	type Request struct {
		Name  *string  `json:"name"`
		Price *float64 `json:"price"`
		Stock *int     `json:"stock"`
		// Barcode was missing from this struct, so PATCH /products/:id
		// silently dropped the field. Two real workflows broke because
		// of it: (1) "Crear producto" via the scanner flow that pre-fills
		// the SKU but PATCHes after the IA-photo step (the product ended
		// up with an empty barcode column), (2) the cashier-side
		// "asociar código a un producto existente" recovery action.
		// Both depend on this column being writable.
		Barcode           *string `json:"barcode"`
		CatalogImageID    *string `json:"catalog_image_id"`
		IsAvailable       *bool   `json:"is_available"`
		RequiresContainer *bool   `json:"requires_container"`
		ContainerPrice    *int64  `json:"container_price"`
		ImageURL          *string `json:"image_url"`
		Presentation      *string `json:"presentation"`
		Content           *string `json:"content"`
		ExpiryDate        *string `json:"expiry_date"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		productID := c.Param("id")

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ?", productID, tenantID).
			First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.Name != nil {
			updates["name"] = *req.Name
		}
		if req.Price != nil {
			updates["price"] = *req.Price
		}
		oldStock := product.Stock
		if req.Stock != nil {
			updates["stock"] = *req.Stock
		}
		if req.Barcode != nil {
			updates["barcode"] = strings.TrimSpace(*req.Barcode)
		}
		if req.IsAvailable != nil {
			updates["is_available"] = *req.IsAvailable
		}
		if req.RequiresContainer != nil {
			updates["requires_container"] = *req.RequiresContainer
		}
		if req.ContainerPrice != nil {
			updates["container_price"] = *req.ContainerPrice
		}
		if req.ImageURL != nil {
			updates["image_url"] = *req.ImageURL
		}
		if req.Presentation != nil {
			updates["presentation"] = *req.Presentation
		}
		if req.Content != nil {
			updates["content"] = *req.Content
		}
		if req.ExpiryDate != nil {
			expiry, err := normaliseExpiryDate(*req.ExpiryDate)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			// nil means "clear the expiry" — store NULL.
			updates["expiry_date"] = expiry
		}

		if err := db.Model(&product).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar producto"})
			return
		}

		if req.Stock != nil && *req.Stock != oldStock {
			services.LogInventoryMovement(db, services.MovementParams{
				TenantID:     tenantID,
				ProductID:    product.ID,
				ProductName:  product.Name,
				MovementType: models.MovementManualAdjust,
				Quantity:     *req.Stock - oldStock,
				UserID:       middleware.UUIDPtr(middleware.GetUserID(c)),
				Notes:        "ajuste manual desde edición de producto",
			})
		}

		// Accept catalog image if provided
		if req.CatalogImageID != nil && *req.CatalogImageID != "" && catalogSvc != nil {
			catalogSvc.AcceptImage(*req.CatalogImageID)
		}

		c.JSON(http.StatusOK, gin.H{"data": product})
	}
}

// RestockProduct atomically increments stock and logs the kardex movement.
// POST /api/v1/products/:id/restock
func RestockProduct(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Quantity      int     `json:"quantity"       binding:"required,min=1"`
		PurchasePrice float64 `json:"purchase_price"`
		Price         float64 `json:"price"`
		ImageURL      string  `json:"image_url"`
		ExpiryDate    string  `json:"expiry_date"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		productID := c.Param("id")

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ?", productID, tenantID).
			First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			services.LogInventoryMovement(tx, services.MovementParams{
				TenantID:      tenantID,
				ProductID:     product.ID,
				ProductName:   product.Name,
				MovementType:  models.MovementInvoiceScan,
				Quantity:      req.Quantity,
				ReferenceType: "invoice",
				UserID:        middleware.UUIDPtr(userID),
			})

			updates := map[string]any{
				"stock": gorm.Expr("stock + ?", req.Quantity),
			}
			if req.PurchasePrice > 0 {
				updates["purchase_price"] = req.PurchasePrice
			}
			if req.Price > 0 {
				updates["price"] = req.Price
			}
			if req.ImageURL != "" {
				updates["image_url"] = req.ImageURL
			}
			expiry, _ := normaliseExpiryDate(req.ExpiryDate)
			if expiry != nil {
				updates["expiry_date"] = *expiry
			}

			return tx.Model(&product).Updates(updates).Error
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al reabastecer"})
			return
		}

		// Reload to return updated stock
		db.First(&product, "id = ?", productID)
		c.JSON(http.StatusOK, gin.H{"data": product})
	}
}

func DeleteProduct(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		productID := c.Param("id")

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ?", productID, tenantID).
			First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
			return
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			// Hard-delete all kardex movements for this product so
			// inventory math stays clean — a soft-deleted product's
			// movements should not count toward totals.
			if err := tx.Unscoped().
				Where("product_id = ? AND tenant_id = ?", productID, tenantID).
				Delete(&models.InventoryMovement{}).Error; err != nil {
				return err
			}
			return tx.Delete(&product).Error
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al eliminar producto"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "producto eliminado"})
	}
}

func SeedProducts(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		samples := []models.Product{
			{TenantID: tenantID, Name: "Coca-Cola 400ml", Price: 2500, Stock: 50, IsAvailable: true, RequiresContainer: true, ContainerPrice: 500},
			{TenantID: tenantID, Name: "Agua Cristal 600ml", Price: 1500, Stock: 30, IsAvailable: true},
			{TenantID: tenantID, Name: "Paquete Papas Margarita", Price: 1800, Stock: 40, IsAvailable: true},
			{TenantID: tenantID, Name: "Chocolatina Jet", Price: 900, Stock: 60, IsAvailable: true},
			{TenantID: tenantID, Name: "Gaseosa Postobón 400ml", Price: 2000, Stock: 45, IsAvailable: true, RequiresContainer: true, ContainerPrice: 500},
			{TenantID: tenantID, Name: "Jabón Protex", Price: 4200, Stock: 20, IsAvailable: true},
			{TenantID: tenantID, Name: "Cuaderno Norma 100h", Price: 6500, Stock: 15, IsAvailable: true},
			{TenantID: tenantID, Name: "Arroz Diana 500g", Price: 3200, Stock: 25, IsAvailable: true},
		}

		if err := db.Create(&samples).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al crear productos de ejemplo"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"message": "productos de ejemplo creados", "count": len(samples)})
	}
}
