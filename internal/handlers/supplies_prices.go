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

// AddSupplyPricesFromInvoice — POST /api/v1/supplies/prices/from-invoice
// Fase 3 (Spec 077): persiste las líneas de una factura YA escaneada (reusa
// ScanInvoice) que el tenant CONFIRMÓ mapeadas a sus insumos, como precios
// source=invoice_ocr (estimado, confianza 0.6, etiquetado "de factura"). El
// match no es automático-silencioso: el cliente confirma cada renglón.
func AddSupplyPricesFromInvoice(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var req struct {
			InvoiceRef   string `json:"invoice_ref"`
			SupplierName string `json:"supplier_name"`
			BranchID     string `json:"branch_id"`
			Items        []struct {
				IngredientID string  `json:"ingredient_id"`
				RawName      string  `json:"raw_name"`
				UnitPrice    float64 `json:"unit_price"`
				PackUnit     string  `json:"pack_unit"`
				PackQty      float64 `json:"pack_qty"`
			} `json:"items"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "datos inválidos"})
			return
		}
		ref := req.InvoiceRef
		if strings.TrimSpace(ref) == "" {
			ref = "factura " + time.Now().Format("2006-01-02")
		}
		rows := make([]models.IngredientPrice, 0, len(req.Items))
		for _, it := range req.Items {
			if strings.TrimSpace(it.IngredientID) == "" || it.UnitPrice <= 0 {
				continue // solo renglones confirmados con precio
			}
			perBase := it.UnitPrice
			if it.PackQty > 0 {
				perBase = it.UnitPrice / it.PackQty
			}
			ingID := it.IngredientID
			rows = append(rows, models.IngredientPrice{
				TenantID: tenantID, BranchID: req.BranchID, IngredientID: &ingID,
				RawName: it.RawName, Source: models.PriceSourceInvoiceOCR,
				SupplierName: req.SupplierName, UnitPrice: it.UnitPrice,
				PackUnit: it.PackUnit, PackQty: it.PackQty, PricePerBaseUnit: perBase,
				Currency: "COP", Confidence: 0.6, CapturedAt: time.Now(), SourceRef: ref,
			})
		}
		if len(rows) > 0 {
			if err := db.Create(&rows).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudieron guardar los precios"})
				return
			}
		}
		c.JSON(http.StatusCreated, gin.H{"data": gin.H{"saved": len(rows)}})
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
