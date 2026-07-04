// Spec: specs/095-variantes-producto/spec.md
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// CreateVariantGroup — POST /api/v1/product-variant-groups (Spec 095).
func CreateVariantGroup(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Name            string   `json:"name" binding:"required"`
		Category        string   `json:"category"`
		ImageURL        string   `json:"image_url"`
		AttributeLabels []string `json:"attribute_labels"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		labels, _ := json.Marshal(req.AttributeLabels)
		group := models.ProductVariantGroup{
			TenantID:        tenantID,
			Name:            req.Name,
			Category:        req.Category,
			ImageURL:        req.ImageURL,
			AttributeLabels: string(labels),
		}
		if err := db.Create(&group).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo crear el grupo"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": group})
	}
}

// ListVariantGroups — GET /api/v1/product-variant-groups (Spec 095).
func ListVariantGroups(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var groups []models.ProductVariantGroup
		db.Where("tenant_id = ?", tenantID).Order("name ASC").Find(&groups)
		c.JSON(http.StatusOK, gin.H{"data": groups})
	}
}

// GetVariantGroup — GET /api/v1/product-variant-groups/:id (Spec 095).
// Siempre scoped a tenant_id (Art. III) — un id de otro tenant da 404, nunca
// 403 con detalle, para no confirmar la existencia del recurso ajeno.
func GetVariantGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var group models.ProductVariantGroup
		if err := db.Where("id = ? AND tenant_id = ?", c.Param("id"), tenantID).
			First(&group).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "grupo no encontrado"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": group})
	}
}

// UpdateVariantGroup — PATCH /api/v1/product-variant-groups/:id (Spec 095).
func UpdateVariantGroup(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Name            *string   `json:"name"`
		Category        *string   `json:"category"`
		ImageURL        *string   `json:"image_url"`
		AttributeLabels *[]string `json:"attribute_labels"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var group models.ProductVariantGroup
		if err := db.Where("id = ? AND tenant_id = ?", c.Param("id"), tenantID).
			First(&group).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "grupo no encontrado"})
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
		if req.Category != nil {
			updates["category"] = *req.Category
		}
		if req.ImageURL != nil {
			updates["image_url"] = *req.ImageURL
		}
		if req.AttributeLabels != nil {
			labels, _ := json.Marshal(*req.AttributeLabels)
			updates["attribute_labels"] = string(labels)
		}
		if len(updates) > 0 {
			if err := db.Model(&group).Where("id = ? AND tenant_id = ?", group.ID, tenantID).
				Updates(updates).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo actualizar el grupo"})
				return
			}
		}
		db.Where("id = ? AND tenant_id = ?", group.ID, tenantID).First(&group)
		c.JSON(http.StatusOK, gin.H{"data": group})
	}
}

// DeleteVariantGroup — DELETE /api/v1/product-variant-groups/:id (Spec 095).
// Rechaza con 409 si el grupo tiene variantes vivas (AC-09) — evita que
// productos con venta activa queden apuntando a un grupo muerto sin nombre
// ni atributos para armar el selector.
func DeleteVariantGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var group models.ProductVariantGroup
		if err := db.Where("id = ? AND tenant_id = ?", c.Param("id"), tenantID).
			First(&group).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "grupo no encontrado"})
			return
		}
		var liveVariants int64
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND variant_group_id = ?", tenantID, group.ID).
			Count(&liveVariants)
		if liveVariants > 0 {
			c.JSON(http.StatusConflict, gin.H{
				"error": "este grupo todavía tiene variantes activas — quítalas o adóptalas a otro grupo primero",
			})
			return
		}
		if err := db.Delete(&group).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo eliminar el grupo"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "grupo eliminado"})
	}
}

// AdoptProductToVariantGroup — POST /api/v1/products/:id/adopt-variant-group
// (Spec 095, AC-03). Vincula un producto YA EXISTENTE a un grupo con un
// simple UPDATE — nunca recrea la fila, así que ventas/kardex/órdenes de
// compra que ya la referencian por ProductID quedan intactos.
func AdoptProductToVariantGroup(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		VariantGroupID    string            `json:"variant_group_id" binding:"required"`
		VariantAttributes map[string]string `json:"variant_attributes"`
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
		var group models.ProductVariantGroup
		if err := db.Where("id = ? AND tenant_id = ?", req.VariantGroupID, tenantID).
			First(&group).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "grupo no encontrado"})
			return
		}

		attrs, _ := json.Marshal(req.VariantAttributes)
		updates := map[string]any{
			"variant_group_id":   req.VariantGroupID,
			"variant_attributes": string(attrs),
		}
		if err := db.Model(&product).Where("id = ? AND tenant_id = ?", productID, tenantID).
			Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo vincular el producto"})
			return
		}
		db.Where("id = ? AND tenant_id = ?", productID, tenantID).First(&product)
		c.JSON(http.StatusOK, gin.H{"data": product})
	}
}

// GenerateVariantCombinations — POST
// /api/v1/product-variant-groups/:id/generate-combinations (Spec 095,
// AC-02/AC-04). Crea el producto cruzado cartesiano de los valores por
// atributo (ej. 2 tallas x 2 colores = 4 productos) en vez de exigir alta
// manual repetida — el generador es la mitigación del riesgo "carga
// incremental multiplica el trabajo" que encontró la verificación
// adversarial de UX. Cada producto creado lleva su propio movimiento de
// kardex inicial, igual que CreateProduct (Art. VII).
func GenerateVariantCombinations(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Attributes map[string][]string `json:"attributes" binding:"required"`
		BasePrice  float64              `json:"base_price" binding:"required,gt=0"`
		BaseStock  int                  `json:"base_stock"`
	}
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		groupID := c.Param("id")

		var group models.ProductVariantGroup
		if err := db.Where("id = ? AND tenant_id = ?", groupID, tenantID).
			First(&group).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "grupo no encontrado"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if len(req.Attributes) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "debe indicar al menos un atributo con valores"})
			return
		}

		combinations := cartesianAttributeCombinations(req.Attributes)
		created := make([]models.Product, 0, len(combinations))

		err := db.Transaction(func(tx *gorm.DB) error {
			for _, combo := range combinations {
				attrs, _ := json.Marshal(combo)
				product := models.Product{
					TenantID:          tenantID,
					Name:              group.Name + " " + variantSuffix(combo),
					Price:             req.BasePrice,
					Stock:             req.BaseStock,
					IsAvailable:       true,
					Category:          group.Category,
					ImageURL:          group.ImageURL,
					VariantGroupID:    &group.ID,
					VariantAttributes: string(attrs),
				}
				if err := tx.Create(&product).Error; err != nil {
					return err
				}
				if product.Stock > 0 {
					zero := float64(0)
					initial := float64(product.Stock)
					if err := services.LogInventoryMovement(tx, services.MovementParams{
						TenantID:            tenantID,
						ProductID:           product.ID,
						ProductName:         product.Name,
						MovementType:        models.MovementInitialStock,
						Quantity:            product.Stock,
						UserID:              middleware.UUIDPtr(userID),
						StockBeforeOverride: &zero,
						StockAfterOverride:  &initial,
					}); err != nil {
						return err
					}
				}
				created = append(created, product)
			}
			return nil
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudieron crear las variantes"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": created})
	}
}

// cartesianAttributeCombinations arma el producto cartesiano de los valores
// por atributo, ej. {"Talla":["S","M"],"Color":["Rojo","Azul"]} → 4 mapas.
// Orden determinístico (por eso itera claves ordenadas) para que la salida
// sea estable entre llamadas con el mismo input.
func cartesianAttributeCombinations(attrs map[string][]string) []map[string]string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sortStrings(keys)

	combos := []map[string]string{{}}
	for _, key := range keys {
		values := attrs[key]
		next := make([]map[string]string, 0, len(combos)*len(values))
		for _, existing := range combos {
			for _, v := range values {
				combo := make(map[string]string, len(existing)+1)
				for k, ev := range existing {
					combo[k] = ev
				}
				combo[key] = v
				next = append(next, combo)
			}
		}
		combos = next
	}
	return combos
}

// variantSuffix arma un sufijo legible tipo "Talla M, Color Rojo" a partir
// de la combinación, para el nombre del producto generado.
func variantSuffix(combo map[string]string) string {
	keys := make([]string, 0, len(combo))
	for k := range combo {
		keys = append(keys, k)
	}
	sortStrings(keys)
	suffix := ""
	for i, k := range keys {
		if i > 0 {
			suffix += ", "
		}
		suffix += combo[k]
	}
	return suffix
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// blockIfOpenPurchaseOrder rechaza desactivar/borrar un producto que una
// orden de compra ABIERTA (borrador/enviada) todavía referencia — sin esto,
// el stock recibido después entraría a una fila abandonada y nunca llegaría
// a las variantes que el tendero creó en su lugar (hallazgo de la
// verificación adversarial de integridad de datos).
func blockIfOpenPurchaseOrder(db *gorm.DB, tenantID, productID string) error {
	var count int64
	db.Model(&models.PurchaseOrderItem{}).
		Joins("JOIN purchase_orders ON purchase_orders.id = purchase_order_items.purchase_order_id").
		Where("purchase_order_items.product_id = ? AND purchase_orders.tenant_id = ? AND purchase_orders.status IN ?",
			productID, tenantID, []string{models.PurchaseOrderDraft, models.PurchaseOrderSent}).
		Count(&count)
	if count > 0 {
		return errors.New("este producto tiene una orden de compra abierta — recíbela o cancélala antes de desactivarlo")
	}
	return nil
}
