package handlers

import (
	"fmt"
	"net/http"
	"time"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

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

		// Credit sales need either a customer (opens a new credit account) or
		// an existing credit_account_id (appends to a pre-authorized fiado).
		hasCustomer := req.CustomerID != nil && *req.CustomerID != ""
		hasCreditAccount := req.CreditAccountID != nil && *req.CreditAccountID != ""
		if req.PaymentMethod == models.PaymentCredit && !hasCustomer && !hasCreditAccount {
			c.JSON(http.StatusBadRequest, gin.H{"error": "customer_id o credit_account_id requerido para ventas a crédito"})
			return
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
				hasCustomer = true
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

		// Snapshot customer identity BEFORE the transaction so reprinting
		// an old receipt doesn't depend on the Customer row still matching.
		customerNameSnap, customerPhoneSnap := "", ""
		if req.CustomerID != nil && *req.CustomerID != "" {
			var customer models.Customer
			if err := db.Select("name", "phone").
				Where("id = ? AND tenant_id = ?", *req.CustomerID, tenantID).
				First(&customer).Error; err == nil {
				customerNameSnap = customer.Name
				customerPhoneSnap = customer.Phone
			}
		}

		var sale models.Sale
		err := db.Transaction(func(tx *gorm.DB) error {
			var items []models.SaleItem
			var total float64

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

				if product.Stock > 0 {
					tx.Model(&product).UpdateColumn("stock", gorm.Expr("stock - ?", item.Quantity))
				}
			}

			// Tax and tip ride on top of the item total. Keep them
			// separate on the row so reports can strip them out.
			total += req.TaxAmount + req.TipAmount

			paymentStatus := req.PaymentStatus
			if paymentStatus == "" {
				paymentStatus = "COMPLETED"
			}
			sale = models.Sale{
				TenantID:              tenantID,
				CreatedBy:             middleware.UUIDPtr(userID),
				BranchID:              middleware.UUIDPtr(branchID),
				Total:                 total,
				TaxAmount:             req.TaxAmount,
				TipAmount:             req.TipAmount,
				PaymentMethod:         req.PaymentMethod,
				CustomerID:            req.CustomerID,
				CustomerNameSnapshot:  customerNameSnap,
				CustomerPhoneSnapshot: customerPhoneSnap,
				IsCredit:              req.PaymentMethod == models.PaymentCredit,
				CreditAccountID:       req.CreditAccountID,
				PaymentStatus:         paymentStatus,
				DynamicQRPayload:      req.DynamicQRPayload,
				Items:                 items,
			}
			if req.ID != "" {
				sale.ID = req.ID
			}

			if err := tx.Create(&sale).Error; err != nil {
				return err
			}

			// Only create a new credit account when the caller did NOT pass
			// an existing one. When credit_account_id is present the append
			// is authoritative (InitFiado / AppendToFiado already bumped the
			// total); we just link the sale so the statement can show items.
			if sale.IsCredit && req.CustomerID != nil && !hasCreditAccount {
				credit := models.CreditAccount{
					TenantID:    tenantID,
					CreatedBy:   middleware.UUIDPtr(userID),
					BranchID:    middleware.UUIDPtr(branchID),
					CustomerID:  *req.CustomerID,
					SaleID:      &sale.ID,
					TotalAmount: int64(total),
					Status:      "open",
				}
				return tx.Create(&credit).Error
			}

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

		now := time.Now()
		startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		startOfYesterday := startOfToday.AddDate(0, 0, -1)

		var totalSales float64
		var transactionCount int64

		db.Model(&models.Sale{}).
			Where("tenant_id = ? AND created_at >= ? AND deleted_at IS NULL", tenantID, startOfToday).
			Count(&transactionCount).
			Select("COALESCE(SUM(total), 0)").
			Scan(&totalSales)

		var yesterdaySales float64
		db.Model(&models.Sale{}).
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
