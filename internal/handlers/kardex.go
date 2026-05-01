package handlers

import (
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ProductKardex returns the movement history for a single product.
// GET /api/v1/inventory/kardex?product_id=X
func ProductKardex(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		productID := c.Query("product_id")
		if productID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "product_id requerido"})
			return
		}

		scope := ResolveBranchScope(c, db)
		if scope.NotOwned {
			c.JSON(http.StatusForbidden, gin.H{
				"error":      "la sucursal no pertenece al negocio",
				"error_code": "branch_not_owned",
			})
			return
		}

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ?", productID, tenantID).
			First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
			return
		}

		p := parsePagination(c)
		query := db.Model(&models.InventoryMovement{}).
			Where("tenant_id = ? AND product_id = ?", tenantID, productID)
		query = applyBranchScopeMovements(query, scope)

		var total int64
		query.Count(&total)

		var movements []models.InventoryMovement
		if err := query.Order("created_at DESC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&movements).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener movimientos"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"product": gin.H{
					"id":           product.ID,
					"name":         product.Name,
					"stock":        product.Stock,
					"barcode":      product.Barcode,
					"presentation": product.Presentation,
					"content":      product.Content,
				},
				"movements": movements,
				"total":     total,
				"page":      p.Page,
				"per_page":  p.PerPage,
			},
		})
	}
}

// InventoryReport returns a general inventory report with all products,
// their current stock, and movement summaries. Branch-scoped.
// GET /api/v1/inventory/report
func InventoryReport(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)
		if scope.NotOwned {
			c.JSON(http.StatusForbidden, gin.H{
				"error":      "la sucursal no pertenece al negocio",
				"error_code": "branch_not_owned",
			})
			return
		}

		p := parsePagination(c)

		// Count total products for pagination
		prodQuery := db.Model(&models.Product{}).
			Where("tenant_id = ? AND is_available = true", tenantID)
		prodQuery = ApplyBranchScope(prodQuery, scope)

		var totalProducts int64
		prodQuery.Count(&totalProducts)

		// Get products with pagination
		var products []models.Product
		listQ := db.Where("tenant_id = ? AND is_available = true", tenantID)
		listQ = ApplyBranchScope(listQ, scope)
		if err := listQ.Order("name ASC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&products).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener productos"})
			return
		}

		if len(products) == 0 {
			c.JSON(http.StatusOK, gin.H{
				"data": gin.H{
					"products":       []any{},
					"total_products": 0,
					"page":           p.Page,
					"per_page":       p.PerPage,
					"branch":         branchInfo(db, scope),
				},
			})
			return
		}

		// Collect product IDs for batch query
		productIDs := make([]string, len(products))
		for i, pr := range products {
			productIDs[i] = pr.ID
		}

		// Aggregate movements per product: total_in, total_out, last_movement
		type MovementSummary struct {
			ProductID    string `gorm:"column:product_id"`
			TotalIn      int    `gorm:"column:total_in"`
			TotalOut     int    `gorm:"column:total_out"`
			LastMovement string `gorm:"column:last_movement"`
		}

		var summaries []MovementSummary
		movQ := db.Model(&models.InventoryMovement{}).
			Select(`product_id,
				COALESCE(SUM(CASE WHEN quantity > 0 THEN quantity ELSE 0 END), 0) AS total_in,
				COALESCE(SUM(CASE WHEN quantity < 0 THEN ABS(quantity) ELSE 0 END), 0) AS total_out,
				MAX(created_at)::text AS last_movement`).
			Where("tenant_id = ? AND product_id IN ?", tenantID, productIDs)
		movQ = applyBranchScopeMovements(movQ, scope)
		movQ.Group("product_id").Scan(&summaries)

		summaryMap := map[string]MovementSummary{}
		for _, s := range summaries {
			summaryMap[s.ProductID] = s
		}

		type ProductReport struct {
			ID           string  `json:"id"`
			Name         string  `json:"name"`
			Barcode      string  `json:"barcode,omitempty"`
			Presentation string  `json:"presentation,omitempty"`
			Content      string  `json:"content,omitempty"`
			Stock        int     `json:"stock"`
			MinStock     int     `json:"min_stock"`
			Price        float64 `json:"price"`
			TotalIn      int     `json:"total_in"`
			TotalOut     int     `json:"total_out"`
			LastMovement string  `json:"last_movement,omitempty"`
		}

		var report []ProductReport
		for _, pr := range products {
			entry := ProductReport{
				ID:           pr.ID,
				Name:         pr.Name,
				Barcode:      pr.Barcode,
				Presentation: pr.Presentation,
				Content:      pr.Content,
				Stock:        pr.Stock,
				MinStock:     pr.MinStock,
				Price:        pr.Price,
			}
			if s, ok := summaryMap[pr.ID]; ok {
				entry.TotalIn = s.TotalIn
				entry.TotalOut = s.TotalOut
				entry.LastMovement = s.LastMovement
			}
			report = append(report, entry)
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"products":       report,
				"total_products": totalProducts,
				"page":           p.Page,
				"per_page":       p.PerPage,
				"branch":         branchInfo(db, scope),
			},
		})
	}
}

// MatchProductsHandler exposes the smart deduplication algorithm.
// POST /api/v1/inventory/match-products
func MatchProductsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req struct {
			Products []services.MatchProductRequest `json:"products" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if len(req.Products) > 50 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "máximo 50 productos por solicitud"})
			return
		}

		results := services.MatchProducts(db, tenantID, req.Products)

		c.JSON(http.StatusOK, gin.H{"data": results})
	}
}

// applyBranchScopeMovements applies branch scope to inventory_movements queries.
func applyBranchScopeMovements(q *gorm.DB, scope BranchScopeResolution) *gorm.DB {
	if scope.BranchID != "" {
		return q.Where("branch_id = ?", scope.BranchID)
	}
	return q
}

// branchInfo returns the branch metadata for the report header.
func branchInfo(db *gorm.DB, scope BranchScopeResolution) gin.H {
	if scope.BranchID == "" {
		return gin.H{"id": nil, "name": "Principal (todas las sedes)"}
	}
	var branch models.Branch
	if err := db.Where("id = ?", scope.BranchID).First(&branch).Error; err == nil {
		return gin.H{"id": branch.ID, "name": branch.Name}
	}
	return gin.H{"id": scope.BranchID, "name": "Sede"}
}
