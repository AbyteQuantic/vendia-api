// Spec: specs/029-precios-multi-tier/spec.md (PriceTier wiring)
// Spec: specs/030-administracion-clientes-no-tienda/spec.md (customer_id validation)
package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// isAbsoluteHTTPURL is a tiny audit-friendly check on receipt image
// URLs. We never reject the transaction over a malformed URL — the
// cashier shouldn't lose a sale because Supabase returned an unusual
// host — but we do log a warning so anomalies are visible in the
// observability stream. Empty string is treated as "not provided" and
// is allowed (cash sales legitimately omit it).
func isAbsoluteHTTPURL(s string) bool {
	if s == "" {
		return true
	}
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// SaleItemRequest represents either a product line or an ad-hoc service
// line. Validation is performed in CreateSale since `binding:"required"`
// can't express the XOR between product_id and (is_service + custom_*).
type SaleItemRequest struct {
	// ProductID is required for physical inventory lines. Leave empty for
	// service/custom lines (then IsService must be true).
	ProductID    string `json:"product_id"`
	Quantity     int    `json:"quantity"   binding:"required,min=1"`
	HasContainer *bool  `json:"has_container"`

	// Service billing (migration 020). When IsService=true, the backend
	// skips the products lookup and bills CustomDescription +
	// CustomUnitPrice ad-hoc — no stock deduction, no product FK.
	IsService         bool    `json:"is_service"`
	CustomDescription string  `json:"custom_description"`
	CustomUnitPrice   float64 `json:"custom_unit_price"`
}

type CreateSaleRequest struct {
	ID            string               `json:"id"`
	Items         []SaleItemRequest    `json:"items"          binding:"required,min=1"`
	PaymentMethod models.PaymentMethod `json:"payment_method" binding:"required"`
	CustomerID    *string              `json:"customer_id"`
	// CreditAccountID links this sale to an already-open fiado — set by the
	// client when the cashier picks "Agregar a cuenta existente" in checkout.
	// When present we skip the auto-create-credit step and just link.
	CreditAccountID *string `json:"credit_account_id"`
	// PaymentStatus / DynamicQRPayload — present only for the zero-fee
	// dynamic QR flow (Nequi/Daviplata/Bancolombia transfers). Cash sales
	// leave them nil and default to 'COMPLETED'.
	PaymentStatus    string  `json:"payment_status"`
	DynamicQRPayload *string `json:"dynamic_qr_payload"`

	// TaxAmount / TipAmount are added on top of the item subtotals. Kept
	// nullable so callers that don't compute them (legacy cashier flow)
	// can send zero. Stored on the Sale row directly for reporting.
	TaxAmount float64 `json:"tax_amount"`
	TipAmount float64 `json:"tip_amount"`

	// BranchID (Phase 6) — the sede the sale is registered against.
	// When provided the handler scopes product lookup + stock
	// decrement to this branch's inventory. Optional for
	// backward-compat with mono-sede tenants whose POS never knew
	// about sedes; in that case we fall back to the JWT's branch
	// claim, and if that's empty too, to the global-scope lookup
	// that existed before the isolation refactor.
	BranchID string `json:"branch_id"`

	// ReceiptImageURL — Supabase Storage URL of the photo the cashier
	// took of the digital-payment confirmation (Mandatory Image
	// Receipts epic). Optional from the backend's perspective: the
	// frontend enforces "obligatorio para pagos digitales", but here
	// we stay informative so we never block an audit-friendly cash
	// sale. Pointer so we can distinguish "not sent" from "explicit
	// empty string" if a future client cares.
	ReceiptImageURL *string `json:"receipt_image_url"`

	// Spec F029 — PriceTier records which tier the cashier picked in
	// "Confirmar Venta". Optional: omitted/empty → defaults to 'retail'
	// (retrocompat: pre-F029 clients keep working). Must match the enum
	// {retail, tier_1, tier_2, tier_3} when provided.
	PriceTier string `json:"price_tier"`
}

func CreateSale(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		userID := middleware.GetUserID(c)
		jwtBranchID := middleware.GetBranchID(c)

		var req CreateSaleRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Resolve the branch this sale is scoped to. Priority:
		// payload > JWT claim > none. Empty string signals "no
		// scope" — mono-sede tenants who send neither keep the
		// global product lookup so stock isn't hidden from them.
		branchID := jwtBranchID
		if req.BranchID != "" {
			if !models.IsValidUUID(req.BranchID) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "branch_id debe ser un UUID válido",
				})
				return
			}
			// Ownership check — a crafted payload could try to point
			// at another tenant's sede. The BranchScopeResolution
			// helper used by list endpoints reads from the query
			// string; here we re-run the same ownership check on
			// the body value.
			var ownedCount int64
			db.Model(&models.Branch{}).
				Where("id = ? AND tenant_id = ?", req.BranchID, tenantID).
				Count(&ownedCount)
			if ownedCount == 0 {
				c.JSON(http.StatusForbidden, gin.H{
					"error":      "la sucursal no pertenece al negocio",
					"error_code": "branch_not_owned",
				})
				return
			}
			branchID = req.BranchID
		}

		if req.ID != "" && !models.IsValidUUID(req.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id must be a valid UUID v4"})
			return
		}

		// Spec F029 — validate price_tier enum. Empty string is the
		// retrocompat default ('retail'), normalised below before
		// inserting. Any other value outside the four canonical members
		// is rejected with a Spanish 400 so the contract stays clear at
		// the API boundary (instead of letting the DB CHECK throw 500).
		if req.PriceTier != "" && !models.IsValidPriceTier(req.PriceTier) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "price_tier inválido: debe ser uno de 'retail', 'tier_1', 'tier_2', 'tier_3'",
			})
			return
		}

		// Feature 004 / BUG-2 — idempotent re-sync. The sale UUID is
		// generated client-side (Art. II offline-first); an offline POS
		// that re-syncs a sale it already persisted would otherwise hit
		// the sales_pkey UNIQUE constraint inside tx.Create below and
		// the handler would leak a raw Postgres `duplicate key` error
		// as HTTP 400 — English, ugly, and a dead end for the tendero.
		//
		// Instead: if a client provided an `id` that already belongs to
		// a live (non-soft-deleted) sale for THIS tenant, the operation
		// already succeeded. Return that existing sale with HTTP 200 and
		// stop here. The first sale wins (spec D1): we never overwrite
		// it with the new payload. Because this return happens BEFORE
		// db.Transaction opens, the recipe explosion never runs on the
		// duplicate path — insumos are not discounted a second time
		// (AC-02). The query is tenant-scoped (Art. III) and GORM's
		// default soft-delete scope keeps it to live sales only.
		if req.ID != "" {
			var existing models.Sale
			lookupErr := db.Preload("Items").
				Where("id = ? AND tenant_id = ?", req.ID, tenantID).
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
					"error": "no se pudo verificar la venta",
				})
				return
			}
			// Clean miss → fresh sale, fall through to normal creation.
		}

		// Credit sales MUST carry an existing credit_account_id. The
		// fiado handshake is the ONLY path that opens a CreditAccount
		// — see /api/v1/fiado/init — so a credit sale without that id
		// is a client bug we want to surface as 400 instead of silently
		// patching it up here. Without this guard we'd create rogue
		// ledger accounts that never went through the handshake (no
		// customer signature, no notification, no audit trail).
		hasCustomer := req.CustomerID != nil && *req.CustomerID != ""
		hasCreditAccount := req.CreditAccountID != nil && *req.CreditAccountID != ""
		if req.PaymentMethod == models.PaymentCredit {
			if !hasCreditAccount {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "credit_account_id requerido — abre el fiado vía /api/v1/fiado/init",
				})
				return
			}
		}

		// When the sale is linked to an existing credit account but the
		// client did not send customer_id, inherit it from the credit so
		// analytics still attribute the purchase to the right customer.
		if hasCreditAccount && !hasCustomer {
			var linked models.CreditAccount
			if err := db.Select("customer_id").
				Where("id = ? AND tenant_id = ?", *req.CreditAccountID, tenantID).
				First(&linked).Error; err == nil && linked.CustomerID != "" {
				cid := linked.CustomerID
				req.CustomerID = &cid
			}
		}

		// Validate item shape up-front so we never begin a transaction
		// with a guaranteed-to-fail line (keeps DB connection usage down
		// on obvious client bugs).
		for i, item := range req.Items {
			if err := validateSaleItemRequest(item); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("item %d: %s", i+1, err.Error())})
				return
			}
		}

		// Spec F030 — validate the optional customer_id and snapshot the
		// customer identity BEFORE the transaction. Reprinting an old
		// receipt must not depend on the Customer row still matching, so
		// name/phone are frozen onto the sale.
		//
		// Three cases:
		//   - customer_id absent or empty string → anonymous sale. We
		//     normalise it to a nil pointer via middleware.UUIDPtr so
		//     GORM emits SQL NULL instead of an empty-string literal that
		//     Postgres rejects on the uuid column (feedback_nullable_uuid_rule).
		//   - customer_id present but not a UUID, or not owned by this
		//     tenant → 404. Rejecting (instead of the pre-F030 behaviour
		//     of silently persisting a blank snapshot) keeps a crafted or
		//     stale payload from attaching a sale to another tenant's
		//     customer (Constitución Art. III + VI).
		//   - customer_id present and owned → snapshot + keep the link.
		customerNameSnap, customerPhoneSnap := "", ""
		var customerIDPtr *string
		if req.CustomerID != nil && *req.CustomerID != "" {
			cid := strings.TrimSpace(*req.CustomerID)
			if !models.IsValidUUID(cid) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "customer_id debe ser un UUID válido",
				})
				return
			}
			var customer models.Customer
			if err := db.Select("name", "phone").
				Where("id = ? AND tenant_id = ?", cid, tenantID).
				First(&customer).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					c.JSON(http.StatusNotFound, gin.H{
						"error": "cliente no encontrado",
					})
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "no se pudo verificar el cliente",
				})
				return
			}
			customerNameSnap = customer.Name
			customerPhoneSnap = customer.Phone
			customerIDPtr = middleware.UUIDPtr(cid)
		}

		// saleInventory applies the inventory side effects of the sale —
		// direct-product stock decrement + `sale` movement, and recipe
		// explosion for product-recetas — AFTER the sale row is persisted.
		// FR-02: the same service is invoked by CloseOrder so a KDS order
		// close discounts inventory identically (Constitución Art. IX —
		// one implementation, two callers).
		saleInventory := services.NewSaleInventoryService(db)

		var sale models.Sale
		err := db.Transaction(func(tx *gorm.DB) error {
			var items []models.SaleItem
			var total float64
			// inventoryLines collects every product line (direct AND
			// recipe) so the shared SaleInventoryService applies the
			// stock effects once the sale row exists (its UUID is the
			// idempotency anchor for the recipe explosion).
			var inventoryLines []services.SaleInventoryLine

			for _, item := range req.Items {
				if item.IsService {
					subtotal := item.CustomUnitPrice * float64(item.Quantity)
					total += subtotal
					items = append(items, models.SaleItem{
						ProductID:         nil,
						Name:              item.CustomDescription,
						Price:             item.CustomUnitPrice,
						Quantity:          item.Quantity,
						Subtotal:          subtotal,
						IsService:         true,
						CustomDescription: item.CustomDescription,
						CustomUnitPrice:   item.CustomUnitPrice,
					})
					continue
				}

				var product models.Product
				// Scope the product lookup to the selected sede when
				// one is set. Same product UUID can exist in two
				// sedes with independent stock counters — filtering
				// by branch_id here is what makes Phase-6 isolation
				// real instead of advisory. When branchID is empty
				// (mono-sede tenants) we keep the tenant-wide query
				// for backward-compat.
				q := tx.Where(
					"id = ? AND tenant_id = ? AND is_available = true",
					item.ProductID, tenantID)
				if branchID != "" {
					q = q.Where("branch_id = ?", branchID)
				}
				if err := q.First(&product).Error; err != nil {
					return &gin.Error{Err: errProductNotFound(item.ProductID), Type: gin.ErrorTypePublic}
				}

				subtotal := product.Price * float64(item.Quantity)
				total += subtotal

				productID := product.ID
				items = append(items, models.SaleItem{
					ProductID: &productID,
					Name:      product.Name,
					Price:     product.Price,
					Quantity:  item.Quantity,
					Subtotal:  subtotal,
				})

				// Every product line — direct or recipe — is queued for
				// the shared SaleInventoryService, applied after the sale
				// row exists. The service itself decides direct-decrement
				// vs recipe-explosion based on Product.IsRecipe, so a
				// direct product still decrements its own stock and a
				// recipe still explodes — behaviour unchanged (AC-06).
				inventoryLines = append(inventoryLines, services.SaleInventoryLine{
					ProductID: product.ID,
					Quantity:  item.Quantity,
				})

				if product.RequiresContainer && (item.HasContainer == nil || !*item.HasContainer) {
					containerSubtotal := float64(product.ContainerPrice) * float64(item.Quantity)
					total += containerSubtotal
					items = append(items, models.SaleItem{
						ProductID:         &productID,
						Name:              product.Name + " — Envase",
						Price:             float64(product.ContainerPrice),
						Quantity:          item.Quantity,
						Subtotal:          containerSubtotal,
						IsContainerCharge: true,
					})
				}

				// NOTE: the direct-product stock decrement + `sale`
				// movement is no longer applied inline. It is applied by
				// saleInventory.ApplyPostSale after the sale row exists,
				// together with the recipe explosion — see below.
			}

			// Tax and tip ride on top of the item total. Keep them
			// separate on the row so reports can strip them out.
			total += req.TaxAmount + req.TipAmount

			paymentStatus := req.PaymentStatus
			if paymentStatus == "" {
				paymentStatus = "COMPLETED"
			}
			// Resolve who is making the sale so the analytics dashboard
			// can attribute it to the right employee (Ranking del
			// equipo). The JWT carries user_id; we look up the User row
			// once for the name. Empty userID means a legacy single-
			// tenant token without user context — falls back to the
			// owner's name on the Tenant row so the dashboard at least
			// shows the dueño instead of "Sin asignar". Cheap query
			// (PK lookup); cached implicitly by the connection pool.
			employeeName := ""
			if userID != "" {
				var u models.User
				if err := db.Select("name").
					Where("id = ?", userID).First(&u).Error; err == nil {
					employeeName = u.Name
				}
			}
			if employeeName == "" {
				var t models.Tenant
				if err := db.Select("owner_name").
					Where("id = ?", tenantID).First(&t).Error; err == nil {
					employeeName = t.OwnerName
				}
			}

			receiptURL := ""
			if req.ReceiptImageURL != nil {
				receiptURL = *req.ReceiptImageURL
				if !isAbsoluteHTTPURL(receiptURL) {
					// Audit-only — never block the sale on a bad URL,
					// but surface it so we can spot a misconfigured
					// client.
					log.Printf("[create-sale] tenant=%s non-absolute receipt_image_url=%q",
						tenantID, receiptURL)
				}
			}

			// Spec F029 — normalise the tier so the persisted row never
			// carries an empty string. The DB default would fire on
			// omit, but GORM serialises non-pointer strings even when
			// blank — pin 'retail' explicitly to keep the contract
			// readable.
			priceTier := req.PriceTier
			if priceTier == "" {
				priceTier = models.PriceTierRetail
			}

			sale = models.Sale{
				TenantID:              tenantID,
				CreatedBy:             middleware.UUIDPtr(userID),
				BranchID:              middleware.UUIDPtr(branchID),
				EmployeeUUID:          middleware.UUIDPtr(userID),
				EmployeeName:          employeeName,
				Total:                 total,
				TaxAmount:             req.TaxAmount,
				TipAmount:             req.TipAmount,
				PaymentMethod:         req.PaymentMethod,
				CustomerID:            customerIDPtr,
				CustomerNameSnapshot:  customerNameSnap,
				CustomerPhoneSnapshot: customerPhoneSnap,
				IsCredit:              req.PaymentMethod == models.PaymentCredit,
				CreditAccountID:       req.CreditAccountID,
				PaymentStatus:         paymentStatus,
				DynamicQRPayload:      req.DynamicQRPayload,
				ReceiptImageURL:       receiptURL,
				PriceTier:             priceTier,
				Items:                 items,
			}
			if req.ID != "" {
				sale.ID = req.ID
			}

			if err := tx.Create(&sale).Error; err != nil {
				return err
			}

			// FR-02 — apply every product line's inventory effect now
			// that the sale row (and its UUID) exists: direct products
			// decrement their own stock and log a `sale` movement,
			// product-recetas explode into insumo consumption. The sale
			// UUID anchors the recipe-explosion idempotency, so a
			// re-synced sale never discounts insumos twice (Art. II). A
			// failure here aborts the transaction so the sale and the
			// inventory stay consistent.
			if err := saleInventory.ApplyPostSale(tx, services.PostSaleParams{
				TenantID: tenantID,
				SaleUUID: sale.ID,
				BranchID: middleware.UUIDPtr(branchID),
				UserID:   middleware.UUIDPtr(userID),
				Lines:    inventoryLines,
			}); err != nil {
				return err
			}

			// NOTE: credit accounts are only ever created via the explicit
			// fiado handshake (POST /api/v1/fiado/init) — never implicitly
			// from a sale. If we got here the credit_account_id was already
			// validated above; we just need the sale row linked to it.
			return nil
		})

		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": sale})
	}
}

func TodayStats(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		scope := ResolveBranchScope(c, db)

		startOfToday := startOfTenantDay(tenantNow())
		startOfYesterday := startOfToday.AddDate(0, 0, -1)

		var totalSales float64
		var transactionCount int64

		ApplyBranchScope(db.Model(&models.Sale{}), scope).
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, startOfToday).
			Count(&transactionCount).
			Select("COALESCE(SUM(total), 0)").
			Scan(&totalSales)

		var yesterdaySales float64
		ApplyBranchScope(db.Model(&models.Sale{}), scope).
			Where("tenant_id = ? AND created_at >= ? AND created_at < ? AND deleted_at IS NULL",
				tenantID, startOfYesterday, startOfToday).
			Select("COALESCE(SUM(total), 0)").
			Scan(&yesterdaySales)

		trend := "primer día"
		if yesterdaySales > 0 {
			pct := ((totalSales - yesterdaySales) / yesterdaySales) * 100
			if pct >= 0 {
				trend = fmt.Sprintf("+%.0f%%", pct)
			} else {
				trend = fmt.Sprintf("%.0f%%", pct)
			}
		} else if totalSales > 0 {
			trend = "+100%"
		}

		type TopProduct struct {
			Name     string `json:"name"`
			Quantity int    `json:"quantity"`
		}
		var top TopProduct
		db.Model(&models.SaleItem{}).
			Select("sale_items.name, SUM(sale_items.quantity) as quantity").
			Joins("JOIN sales ON sales.id = sale_items.sale_id").
			Where("sales.tenant_id = ? AND sales.created_at >= ? AND sales.deleted_at IS NULL", tenantID, startOfToday).
			Group("sale_items.name").
			Order("quantity DESC").
			Limit(1).
			Scan(&top)

		topName := top.Name
		if topName == "" {
			topName = "—"
		}

		c.JSON(http.StatusOK, gin.H{
			"total_sales_today": totalSales,
			"transaction_count": transactionCount,
			"top_product":       topName,
			"trend":             trend,
		})
	}
}

func ListSales(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		p := parsePagination(c)

		scope := ResolveBranchScope(c, db)
		if scope.NotOwned {
			c.JSON(http.StatusForbidden, gin.H{
				"error":      "la sucursal no pertenece al negocio",
				"error_code": "branch_not_owned",
			})
			return
		}

		query := db.Model(&models.Sale{}).Where("tenant_id = ?", tenantID)
		query = ApplyBranchScope(query, scope)

		var total int64
		query.Count(&total)

		var sales []models.Sale
		listQuery := db.Preload("Items").Where("tenant_id = ?", tenantID)
		listQuery = ApplyBranchScope(listQuery, scope)
		if err := listQuery.
			Order("created_at DESC").
			Offset((p.Page - 1) * p.PerPage).
			Limit(p.PerPage).
			Find(&sales).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener ventas"})
			return
		}

		c.JSON(http.StatusOK, newPaginatedResponse(sales, total, p))
	}
}

type productNotFoundError struct{ id string }

func (e *productNotFoundError) Error() string {
	return "producto no encontrado o no disponible"
}

func errProductNotFound(id string) error { return &productNotFoundError{id: id} }

// validateSaleItemRequest enforces the service/product XOR that migration
// 020's CHECK constraint enforces at the DB layer. Surfacing the error
// here means the client sees a Spanish message instead of a 500.
func validateSaleItemRequest(item SaleItemRequest) error {
	if item.IsService {
		if item.ProductID != "" {
			return fmt.Errorf("un ítem de servicio no puede tener product_id")
		}
		if item.CustomDescription == "" {
			return fmt.Errorf("descripción requerida para ítem de servicio")
		}
		if item.CustomUnitPrice <= 0 {
			return fmt.Errorf("precio del servicio debe ser mayor a 0")
		}
		return nil
	}
	if item.ProductID == "" {
		return fmt.Errorf("product_id requerido")
	}
	if !models.IsValidUUID(item.ProductID) {
		return fmt.Errorf("product_id debe ser un UUID v4")
	}
	return nil
}
