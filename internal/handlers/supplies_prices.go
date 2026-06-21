// Spec: specs/077-compra-inteligente-insumos/spec.md
package handlers

import (
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AddSupplyPrice — POST /api/v1/supplies/prices
// Fase 2 (Spec 077): el tenant registra un precio MANUAL de un insumo (de su
// proveedor de preferencia). Append-only (source=manual, fuente garantizada).
// Alimenta el precio sugerido (services.SuggestIngredientPrice).
func AddSupplyPrice(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req struct {
			IngredientID string  `json:"ingredient_id"`
			RawName      string  `json:"raw_name"`
			SupplierID   string  `json:"supplier_id"`
			SupplierName string  `json:"supplier_name"`
			UnitPrice    float64 `json:"unit_price"`
			PackUnit     string  `json:"pack_unit"`
			PackQty      float64 `json:"pack_qty"`
			BranchID     string  `json:"branch_id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.UnitPrice <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Ingrese un precio válido."})
			return
		}
		if strings.TrimSpace(req.IngredientID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Falta el insumo."})
			return
		}

		// price_per_base_unit: si vendió por paquete (ej bulto 50 kg), normaliza.
		perBase := req.UnitPrice
		if req.PackQty > 0 {
			perBase = req.UnitPrice / req.PackQty
		}

		var supplierPtr *string
		if s := strings.TrimSpace(req.SupplierID); s != "" {
			supplierPtr = &s
		}
		ingID := req.IngredientID
		row := models.IngredientPrice{
			TenantID:         tenantID,
			BranchID:         req.BranchID,
			IngredientID:     &ingID,
			RawName:          req.RawName,
			Source:           models.PriceSourceManual,
			SupplierID:       supplierPtr,
			SupplierName:     req.SupplierName,
			UnitPrice:        req.UnitPrice,
			PackUnit:         req.PackUnit,
			PackQty:          req.PackQty,
			PricePerBaseUnit: perBase,
			Currency:         "COP",
			Confidence:       0.8,
			CapturedAt:       time.Now(),
			SourceRef:        "manual",
		}
		if err := db.Create(&row).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo guardar el precio"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": row})
	}
}

// ListSupplyPrices — GET /api/v1/supplies/prices/:ingredientId
// Historial/fuentes de precio conocidas de un insumo (para el comparador y para
// ver "bajó de precio"). Más reciente primero.
func ListSupplyPrices(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var rows []models.IngredientPrice
		db.Where("tenant_id = ? AND ingredient_id = ?", tenantID, c.Param("ingredientId")).
			Order("captured_at DESC").Limit(50).Find(&rows)
		c.JSON(http.StatusOK, gin.H{"data": rows})
	}
}
