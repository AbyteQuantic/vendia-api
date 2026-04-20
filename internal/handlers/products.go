package handlers

import (
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

		var total int64
		query := db.Model(&models.Product{}).Where("tenant_id = ? AND is_available = true", tenantID)
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

		// Accept catalog image if provided
		if req.CatalogImageID != "" && catalogSvc != nil {
			catalogSvc.AcceptImage(req.CatalogImageID)
		}

		c.JSON(http.StatusCreated, gin.H{"data": product})
	}
}

func UpdateProduct(db *gorm.DB, catalogSvc *services.CatalogService) gin.HandlerFunc {
	type Request struct {
		Name              *string  `json:"name"`
		Price             *float64 `json:"price"`
		Stock             *int     `json:"stock"`
		CatalogImageID    *string  `json:"catalog_image_id"`
		IsAvailable       *bool    `json:"is_available"`
		RequiresContainer *bool    `json:"requires_container"`
		ContainerPrice    *int64   `json:"container_price"`
		ImageURL          *string  `json:"image_url"`
		Presentation      *string  `json:"presentation"`
		Content           *string  `json:"content"`
		ExpiryDate        *string  `json:"expiry_date"`
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
		if req.Stock != nil {
			updates["stock"] = *req.Stock
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

		// Accept catalog image if provided
		if req.CatalogImageID != nil && *req.CatalogImageID != "" && catalogSvc != nil {
			catalogSvc.AcceptImage(*req.CatalogImageID)
		}

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

		if err := db.Delete(&product).Error; err != nil {
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
