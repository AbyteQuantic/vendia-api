package handlers

import (
	"errors"
	"fmt"
	"log"
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

		// is_draft = false: excluye productos creados solo para probar
		// fotos de IA antes de "Guardar" (ver models.Product.IsDraft) — no
		// deben aparecer en el inventario, el POS, ni el autocompletado de
		// "Nuevo Producto" hasta que el tendero confirme guardarlos.
		query := db.Model(&models.Product{}).
			Where("tenant_id = ? AND is_available = true AND is_draft = false", tenantID)
		query = ApplyBranchScope(query, scope)

		// sellable_only (POS / caché Isar): oculta los platos de menú
		// INCOMPLETOS — is_menu_item sin receta con ingredientes, no costeables
		// (Spec 078). El inventario y demás consumidores NO pasan el flag, así
		// que los siguen viendo para poder completarlos. Una receta incompleta
		// no es un plato vendible (reporte fundador 2026-06-24).
		if c.Query("sellable_only") == "true" {
			completeIDs := completeMenuProductIDs(db, tenantID)
			if len(completeIDs) > 0 {
				query = query.Where("is_menu_item = ? OR id IN ?", false, completeIDs)
			} else {
				query = query.Where("is_menu_item = ?", false)
			}
		}

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

		// Spec 080 AC-03: un plato "por porciones" cuyo lote NO es de hoy está
		// agotado hasta re-cocinar → reportamos stock 0 (efectivo). Así el POS
		// lo muestra AGOTADO sin lógica de fechas en el cliente. Es solo de
		// presentación: no toca la BD.
		today := time.Now().Format("2006-01-02")
		for i := range products {
			if products[i].AvailabilityMode == "por_porciones" &&
				(products[i].PreparedDate == nil || *products[i].PreparedDate != today) {
				products[i].Stock = 0
			}
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
		ID    string  `json:"id"`
		Name  string  `json:"name"     binding:"required"`
		Price float64 `json:"price"    binding:"required,gt=0"`
		Stock int     `json:"stock"`
		// Spec 050 — punto de reorden fijable al crear (antes solo por CSV).
		// Dispara la cadena de alerta/pedido cuando stock <= min_stock.
		MinStock          int    `json:"min_stock"`
		Barcode           string `json:"barcode"`
		ImageURL          string `json:"image_url"`
		RequiresContainer bool   `json:"requires_container"`
		ContainerPrice    int64  `json:"container_price"`
		CatalogImageID    string `json:"catalog_image_id"`
		Presentation      string `json:"presentation"`
		Content           string `json:"content"`
		ExpiryDate        string `json:"expiry_date"`
		Category          string `json:"category"`
		// Spec F043 — platos de menú de restaurante.
		Description string `json:"description"`
		Portion     string `json:"portion"`
		IsMenuItem  bool   `json:"is_menu_item"`
		// Spec 068 — características del producto (texto libre, retail).
		Characteristics string `json:"characteristics"`
		// PhotoIsSample: la foto es una MUESTRA generada por IA desde el
		// nombre (ilustración), no la foto real del plato. El catálogo la
		// etiqueta para no engañar al comensal (F043).
		PhotoIsSample bool `json:"photo_is_sample"`
		// Spec F044 — servicio publicable (catálogo unificado).
		IsService bool `json:"is_service"`
		// Spec 084 — comisión por servicio (peluquería/barbería).
		CommissionPct *float64 `json:"commission_pct"`
		// Spec 084 Fase 2 — duración del servicio en minutos (citas).
		DurationMin *int `json:"duration_min"`
		// Spec 063 — venta solo para mayores de 18 (licor, cigarrillos…).
		IsAgeRestricted bool `json:"is_age_restricted"`
		// IsDraft: producto creado SOLO para que el tendero pruebe fotos de
		// IA en "Nuevo Producto" ANTES de tocar "Guardar" — ver comentario
		// en models.Product.IsDraft. false por defecto (una creación normal
		// nunca es borrador).
		IsDraft bool `json:"is_draft"`

		// Spec F029 — optional tier prices. Nullable pointer so we
		// can distinguish "not sent" from "explicit 0" (invalid). When
		// the tenant has EnablePriceTiers=false these are ignored on the
		// read side, but they are accepted/persisted regardless so a
		// merchant can pre-load tier prices and flip the capacity later
		// without losing the data (case borde §9).
		PriceTier1 *float64 `json:"price_tier_1"`
		PriceTier2 *float64 `json:"price_tier_2"`
		PriceTier3 *float64 `json:"price_tier_3"`

		// Auditoría 2026-07-03: un tenant real acumuló hasta 9 copias del
		// mismo producto (mismo nombre+presentación, stocks contradictorios
		// entre sí) — reintentos de "Nuevo Producto" tras un guardado que
		// pareció fallar, y el OCR de menú creando 2 platos cuando la carta
		// trae precio de media porción/porción completa. ForceCreate deja
		// pasar la creación aunque exista un similar — lo manda el cliente
		// tras confirmar explícitamente con el tendero ("crear de todas
		// formas"), o el importador de menú al reintentar un plato que el
		// tendero ya decidió duplicar a propósito.
		ForceCreate bool `json:"force_create"`

		// Spec 095 — variantes de producto (talla/color). Opcionales: NULL/
		// vacío = producto normal, sin cambio de comportamiento (AC-01).
		// Necesarios aquí para que el reintento offline del frontend
		// (ProductPushSync, que reusa este mismo POST) persista el vínculo
		// al grupo si el producto se creó/editó sin conexión.
		VariantGroupID    string `json:"variant_group_id"`
		VariantAttributes string `json:"variant_attributes"`
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
		// Un PLATO de menú o un SERVICIO es de TODA la tienda, no de una sede:
		// se crea GLOBAL (branch_id NULL) para que aparezca en el POS de
		// cualquier sede (Spec 077). Solo el inventario físico va por sede.
		if req.IsMenuItem || req.IsService {
			branchID = ""
		}

		// Auditoría 2026-07-03: aviso de posible duplicado. Compara en el
		// MISMO scope de sede que tendrá el producto nuevo (branchID ya
		// resuelto arriba, incluida la regla de plato/servicio global) por
		// nombre+presentación normalizados — el mismo criterio de identidad
		// que ya usa el importador CSV (Spec 027) para no crear copias. Los
		// borradores (IsDraft, fotos de prueba en "Nuevo Producto" antes de
		// Guardar) quedan exentos: no son productos reales todavía.
		if !req.ForceCreate && !req.IsDraft {
			var candidates []models.Product
			q := db.Where("tenant_id = ? AND is_draft = false", tenantID)
			if branchID != "" {
				q = q.Where("(branch_id = ? OR branch_id IS NULL)", branchID)
			}
			q.Find(&candidates)
			normName := services.NormalizeText(req.Name)
			normPres := strings.TrimSpace(strings.ToLower(req.Presentation))
			for _, cand := range candidates {
				if services.NormalizeText(cand.Name) == normName &&
					strings.TrimSpace(strings.ToLower(cand.Presentation)) == normPres {
					c.JSON(http.StatusConflict, gin.H{
						"error":            "ya existe un producto con ese nombre",
						"error_code":       "duplicate_product",
						"existing_product": cand,
					})
					return
				}
			}
		}

		// Spec 100 / D1 — un código de barras identifica UN producto por
		// tenant: si otro producto vivo ya usa el código entrante → 409 con
		// el dueño (mismo contrato que en UpdateProduct). Trim para que
		// " 777 " y "777" sean el mismo código; excludeID = req.ID cubre el
		// re-sync offline idempotente que reenvía el propio producto.
		barcode := strings.TrimSpace(req.Barcode)
		if owner := findBarcodeOwner(db, tenantID, barcode, req.ID); owner != nil {
			respondDuplicateBarcode(c, owner)
			return
		}

		expiry, err := normaliseExpiryDate(req.ExpiryDate)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Spec F029 — validate tier prices (>0 when provided). Reject
		// 0/negative early instead of relying on the DB; the message stays
		// in Spanish for the tendero. Pointer dereference is safe under
		// the nil guard.
		if err := validateOptionalTierPrice(req.PriceTier1, 1); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := validateOptionalTierPrice(req.PriceTier2, 2); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := validateOptionalTierPrice(req.PriceTier3, 3); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Spec 050 AC-03 — el punto de reorden no puede ser negativo.
		if req.MinStock < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "el stock mínimo no puede ser negativo"})
			return
		}

		// Spec 095 — "" no es JSON válido; un string vacío rompería un
		// futuro json.Unmarshal en el catálogo/frontend. El default real
		// del modelo es '{}'.
		variantAttributes := req.VariantAttributes
		if variantAttributes == "" {
			variantAttributes = "{}"
		}

		product := models.Product{
			TenantID:          tenantID,
			CreatedBy:         middleware.UUIDPtr(userID),
			BranchID:          middleware.UUIDPtr(branchID),
			Name:              req.Name,
			Price:             req.Price,
			Stock:             req.Stock,
			MinStock:          req.MinStock,
			Barcode:           barcode,
			ImageURL:          req.ImageURL,
			IsAvailable:       true,
			RequiresContainer: req.RequiresContainer,
			ContainerPrice:    req.ContainerPrice,
			Presentation:      req.Presentation,
			Content:           req.Content,
			ExpiryDate:        expiry,
			Category:          req.Category,
			Description:       req.Description,
			Portion:           req.Portion,
			Characteristics:   req.Characteristics,
			IsMenuItem:        req.IsMenuItem,
			PhotoIsSample:     req.PhotoIsSample,
			IsService:         req.IsService,
			CommissionPct:     req.CommissionPct,
			DurationMin:       req.DurationMin,
			IsAgeRestricted:   req.IsAgeRestricted,
			IsDraft:           req.IsDraft,
			PriceTier1:        req.PriceTier1,
			PriceTier2:        req.PriceTier2,
			PriceTier3:        req.PriceTier3,
			VariantGroupID:    middleware.UUIDPtr(req.VariantGroupID),
			VariantAttributes: variantAttributes,
		}
		if req.ID != "" {
			product.ID = req.ID
		}

		// Create + kardex movement run inside one transaction: a kardex
		// write failure must roll back the product creation instead of
		// leaving a product row with no audit trail (Art. VII) and a 201
		// response that silently hid the error.
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&product).Error; err != nil {
				return err
			}

			if product.Stock > 0 {
				// FR-03 — the product row already carries stock_inicial, so
				// LogInventoryMovement's self-read would record
				// stock_before=stock_inicial / stock_after=2×stock_inicial.
				// An initial_stock movement always goes from 0 to the full
				// starting quantity: pass that snapshot explicitly.
				zero := float64(0)
				initial := float64(product.Stock)
				return services.LogInventoryMovement(tx, services.MovementParams{
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
			return nil
		}); err != nil {
			// Spec 100 / D1 — carrera pura: dos creaciones simultáneas con el
			// mismo código pasaron el pre-check y el índice único parcial
			// detuvo la segunda. Mismo 409 duplicate_barcode, nunca un 500.
			if IsProductBarcodeUniqueViolation(err) {
				respondDuplicateBarcode(c, findBarcodeOwner(db, tenantID, barcode, product.ID))
				return
			}
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

// ListProductCategories — GET /api/v1/products/categories. Devuelve las
// categorías DISTINTAS que el tenant ya usó, ordenadas por frecuencia (las más
// usadas primero), para sugerirlas al crear/editar y NO perder las existentes
// (Spec 068). Aislado por tenant; excluye vacíos. GORM ya filtra soft-deleted.
func ListProductCategories(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		var cats []string
		db.Model(&models.Product{}).
			Where("tenant_id = ? AND category <> ''", tenantID).
			Group("category").
			Order("COUNT(*) DESC").
			Pluck("category", &cats)
		if cats == nil {
			cats = []string{}
		}
		c.JSON(http.StatusOK, gin.H{"data": cats})
	}
}

func UpdateProduct(db *gorm.DB, catalogSvc *services.CatalogService) gin.HandlerFunc {
	type Request struct {
		Name  *string  `json:"name"`
		Price *float64 `json:"price"`
		Stock *int     `json:"stock"`
		// Spec 050 — punto de reorden editable (parcial). Antes solo CSV.
		MinStock *int `json:"min_stock"`
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
		// Spec 068 — categoría, descripción y características editables (parcial).
		// Punteros: solo se escriben si el cliente los envía → NO se pierde la
		// categoría existente cuando el PATCH no la incluye.
		Category        *string `json:"category"`
		Description     *string `json:"description"`
		Characteristics *string `json:"characteristics"`
		// Spec 063 — alternar "solo mayores de 18" al editar.
		IsAgeRestricted *bool `json:"is_age_restricted"`
		// IsDraft: ver models.Product.IsDraft. create_product_screen.dart
		// lo pone en false al confirmar "Guardar".
		IsDraft *bool `json:"is_draft"`
		// Spec 084 — comisión por servicio (peluquería/barbería).
		CommissionPct *float64 `json:"commission_pct"`
		// Spec 084 Fase 2 — duración del servicio en minutos (citas).
		DurationMin *int `json:"duration_min"`
		// Spec 080 — alternar el modo de venta del plato ('a_demanda' |
		// 'por_porciones'). Volver a 'a_demanda' limpia el lote del día.
		AvailabilityMode *string `json:"availability_mode"`

		// Spec F029 — optional tier prices on PATCH. Same nullable
		// semantics as CreateProduct.
		PriceTier1 *float64 `json:"price_tier_1"`
		PriceTier2 *float64 `json:"price_tier_2"`
		PriceTier3 *float64 `json:"price_tier_3"`

		// Spec 095 — variantes de producto. Ver comentario en CreateProduct.
		VariantGroupID    *string `json:"variant_group_id"`
		VariantAttributes *string `json:"variant_attributes"`
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
		// Spec 080 — modo de venta del plato. Whitelist (solo dos valores) para
		// no escribir basura. Volver a 'a_demanda' limpia el lote del día
		// (prepared_date) → el plato vuelve a estar disponible por receta.
		if req.AvailabilityMode != nil {
			mode := "a_demanda"
			if *req.AvailabilityMode == "por_porciones" {
				mode = "por_porciones"
			}
			updates["availability_mode"] = mode
			if mode == "a_demanda" {
				updates["prepared_date"] = nil
			}
		}
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
		// Spec 050 AC-02/AC-03 — punto de reorden editable, no negativo.
		if req.MinStock != nil {
			if *req.MinStock < 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "el stock mínimo no puede ser negativo"})
				return
			}
			updates["min_stock"] = *req.MinStock
		}
		if req.Barcode != nil {
			barcode := strings.TrimSpace(*req.Barcode)
			// Spec 100 / D1 — un código de barras identifica UN producto por
			// tenant. Si otro producto vivo ya lo usa → 409 con el dueño;
			// re-guardar el propio código (excludeID = productID) no conflictúa.
			if owner := findBarcodeOwner(db, tenantID, barcode, productID); owner != nil {
				respondDuplicateBarcode(c, owner)
				return
			}
			updates["barcode"] = barcode
		}
		if req.IsAvailable != nil {
			// Spec 095 — desactivar un producto con una orden de compra
			// abierta lo deja "huérfano": el stock recibido después nunca
			// llegaría a las variantes que lo reemplazan.
			if !*req.IsAvailable {
				if err := blockIfOpenPurchaseOrder(db, tenantID, productID); err != nil {
					c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
					return
				}
			}
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
		// Spec 068 — categoría/descripción/características (parcial). Trim de la
		// categoría para normalizar (sin lowercasing: respeta lo que ve el tendero).
		if req.Category != nil {
			updates["category"] = strings.TrimSpace(*req.Category)
		}
		if req.DurationMin != nil {
			updates["duration_min"] = *req.DurationMin
		}
		if req.CommissionPct != nil {
			updates["commission_pct"] = *req.CommissionPct
		}
		if req.Description != nil {
			updates["description"] = *req.Description
		}
		if req.Characteristics != nil {
			updates["characteristics"] = *req.Characteristics
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
		if req.IsAgeRestricted != nil {
			updates["is_age_restricted"] = *req.IsAgeRestricted
		}
		// IsDraft: create_product_screen.dart lo manda explícitamente en
		// false al confirmar "Guardar" sobre un producto que se creó como
		// borrador durante las pruebas de foto con IA (ver
		// models.Product.IsDraft). Puntero para no afectar PATCHes que no
		// lo mencionan (ej. el resto de pantallas de edición de inventario).
		if req.IsDraft != nil {
			updates["is_draft"] = *req.IsDraft
		}

		// Spec F029 — tier prices on PATCH. >0 validation matches Create.
		if req.PriceTier1 != nil {
			if err := validateOptionalTierPrice(req.PriceTier1, 1); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			updates["price_tier_1"] = *req.PriceTier1
		}
		if req.PriceTier2 != nil {
			if err := validateOptionalTierPrice(req.PriceTier2, 2); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			updates["price_tier_2"] = *req.PriceTier2
		}
		if req.PriceTier3 != nil {
			if err := validateOptionalTierPrice(req.PriceTier3, 3); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			updates["price_tier_3"] = *req.PriceTier3
		}
		if req.VariantGroupID != nil {
			updates["variant_group_id"] = *req.VariantGroupID
		}
		if req.VariantAttributes != nil {
			attrs := *req.VariantAttributes
			if attrs == "" {
				attrs = "{}"
			}
			updates["variant_attributes"] = attrs
		}

		// Update + kardex movement run inside one transaction. The kardex
		// snapshot is passed explicitly (oldStock → *req.Stock) instead of
		// letting LogInventoryMovement self-read the stock column: by the
		// time it would read, the Updates() above already wrote the NEW
		// value, so a self-read recorded stock_before=new/stock_after=new+
		// delta — a fabricated pair that never existed. The override keeps
		// the audit trail correct regardless of statement order, and the
		// transaction ensures a kardex failure rolls back the stock edit
		// instead of mutating stock with no audit trail (Art. VII).
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&product).Updates(updates).Error; err != nil {
				return err
			}
			if req.Stock != nil && *req.Stock != oldStock {
				before := float64(oldStock)
				after := float64(*req.Stock)
				return services.LogInventoryMovement(tx, services.MovementParams{
					TenantID:            tenantID,
					ProductID:           product.ID,
					ProductName:         product.Name,
					MovementType:        models.MovementManualAdjust,
					Quantity:            *req.Stock - oldStock,
					UserID:              middleware.UUIDPtr(middleware.GetUserID(c)),
					Notes:               "ajuste manual desde edición de producto",
					StockBeforeOverride: &before,
					StockAfterOverride:  &after,
				})
			}
			return nil
		}); err != nil {
			// Spec 100 / D1 — carrera pura: dos ediciones simultáneas pasaron
			// el pre-check y el índice único parcial detuvo la segunda. Se
			// mapea al MISMO 409 duplicate_barcode, nunca un 500 genérico.
			if req.Barcode != nil && IsProductBarcodeUniqueViolation(err) {
				respondDuplicateBarcode(c, findBarcodeOwner(
					db, tenantID, strings.TrimSpace(*req.Barcode), productID))
				return
			}
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
			if err := services.LogInventoryMovement(tx, services.MovementParams{
				TenantID:      tenantID,
				ProductID:     product.ID,
				ProductName:   product.Name,
				MovementType:  models.MovementInvoiceScan,
				Quantity:      req.Quantity,
				ReferenceType: "invoice",
				UserID:        middleware.UUIDPtr(userID),
			}); err != nil {
				return err
			}

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

func DeleteProduct(db *gorm.DB, storageSvc services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		productID := c.Param("id")

		var product models.Product
		if err := db.Where("id = ? AND tenant_id = ?", productID, tenantID).
			First(&product).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "producto no encontrado"})
			return
		}

		// Spec 095 — no borrar un producto que una orden de compra abierta
		// todavía referencia (el stock recibido después entraría a una fila
		// muerta y nunca llegaría a las variantes que la reemplazan).
		if err := blockIfOpenPurchaseOrder(db, tenantID, productID); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
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

		// Reporte del fundador: un producto SIN ninguna venta real es, casi
		// siempre, una referencia mal creada (ej. un borrador de prueba de
		// foto con IA que el tendero decidió borrar) — no una pieza legítima
		// de catálogo. Si su foto ya quedó registrada en el catálogo GLOBAL
		// compartido (registerCatalogImage, en inventory.go, se llama sin
		// chequear IsDraft — CUALQUIER "Quitar fondo"/"Mejorar con
		// IA"/"Crear foto con IA" la registra de inmediato y ya-aceptada),
		// esa foto sigue sugiriéndose a OTROS tenants para siempre, porque
		// no existe ningún vínculo (FK, hook) entre products y
		// catalog_images — borrar el producto por sí solo no la toca.
		// Sin ventas: limpiamos también la contribución de ESTE tenant al
		// catálogo compartido (created_by_tenant_id — nunca la de otro
		// tenant) y el archivo real en R2, que de otro modo queda huérfano
		// para siempre (ningún código borra objetos de R2 al eliminar un
		// producto). Best-effort y FUERA de la transacción de arriba: el
		// producto ya quedó borrado; un fallo aquí no debe revertir eso.
		if product.PhotoURL == "" && product.ImageURL == "" {
			c.JSON(http.StatusOK, gin.H{"message": "producto eliminado"})
			return
		}
		var salesCount int64
		db.Model(&models.SaleItem{}).Where("product_id = ?", productID).Count(&salesCount)
		if salesCount > 0 {
			c.JSON(http.StatusOK, gin.H{"message": "producto eliminado"})
			return
		}
		var images []models.CatalogImage
		db.Where("created_by_tenant_id = ? AND (image_url = ? OR image_url = ?)",
			tenantID, product.PhotoURL, product.ImageURL).Find(&images)
		for _, img := range images {
			if storageSvc != nil && img.StorageKey != "" {
				if delErr := storageSvc.Delete(c.Request.Context(), "product-photos", img.StorageKey); delErr != nil {
					log.Printf("[delete-product] no se pudo borrar %s de R2: %v", img.StorageKey, delErr)
				}
			}
			if delErr := db.Delete(&img).Error; delErr != nil {
				log.Printf("[delete-product] no se pudo borrar catalog_image %s: %v", img.ID, delErr)
			}
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

// validateOptionalTierPrice enforces the Spec F029 invariant that every
// tier price, when provided, must be strictly positive. Nil is a valid
// "not configured" state — the POS will fall back to the retail price
// (with a visual note) when the cashier picks that tier (FR-06).
// tierIndex is the 1-based label used in the Spanish error message.
func validateOptionalTierPrice(p *float64, tierIndex int) error {
	if p == nil {
		return nil
	}
	if *p <= 0 {
		return fmt.Errorf("el precio del tier %d debe ser mayor a 0", tierIndex)
	}
	return nil
}
