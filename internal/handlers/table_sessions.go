package handlers

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// paymentReceiptBucket holds the screenshots customers attach to a
// pending abono ("aquí está mi comprobante de Nequi"). Kept in its
// own bucket so retention rules can diverge from product photos:
// receipts are legal proof, photos are tendero assets.
const paymentReceiptBucket = "payment-receipts"

// maxReceiptBytes caps uploads at 5 MiB. Bigger than the QR (3 MiB)
// because phone screenshots from Nequi / Daviplata can hit that on
// older devices that haven't compressed them yet.
const maxReceiptBytes = 5 << 20

// PublicTableSession is a deliberately narrowed projection of an
// OrderTicket. It omits everything that could identify the tenant
// operator (created_by, employee_uuid, branch_id), the session
// token itself (the caller already has it), delivery PII, and any
// fields the cashier might flip via the authenticated API.
//
// Keep the JSON contract flat so the Next.js page can render it
// with a single fetch — we do not want the web client doing a
// join against the catalog just to show "Mesa 1".
type PublicTableSession struct {
	TableLabel      string             `json:"table_label"`
	Status          models.OrderStatus `json:"status"`
	Type            models.OrderType   `json:"type"`
	Total           float64            `json:"total"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	WaiterCalledAt  *time.Time         `json:"waiter_called_at,omitempty"`
	Items           []PublicTableItem  `json:"items"`
	TenantName      string             `json:"tenant_name,omitempty"`
	TenantBrandLogo string             `json:"tenant_brand_logo,omitempty"`

	// Abonos aprobados (APPROVED). Pending payments are NOT
	// surfaced publicly — they'd tempt the customer to believe
	// they already settled when the tendero hasn't confirmed yet.
	PartialPayments  []PublicAbono `json:"partial_payments"`
	PaidAmount       float64       `json:"paid_amount"`
	RemainingBalance float64       `json:"remaining_balance"`

	// Payment methods the tenant accepts. Exposing them here
	// means the web client can render the "Hacer abono" modal
	// without a second round-trip to /catalog.
	PaymentMethods []PublicPaymentMethodLite `json:"payment_methods"`
}

type PublicTableItem struct {
	ProductName string    `json:"product_name"`
	Quantity    int       `json:"quantity"`
	UnitPrice   float64   `json:"unit_price"`
	Emoji       string    `json:"emoji,omitempty"`
	Subtotal    float64   `json:"subtotal"`
	// AddedAt is the OrderItem.CreatedAt — timestamp the cashier
	// dropped the product into the cuenta. The live-tab UI renders
	// it as "14:35" next to the product name so the customer can
	// audit "a qué horas me cobraron esto".
	AddedAt time.Time `json:"added_at"`
}

// PublicAbono is the public-facing projection of a PartialPayment
// APPROVED row. Omits the operator id and the internal state machine
// vocabulary — web sees amount + method + timestamp + (optional)
// receipt_url so a returning customer can re-open the proof they
// already sent.
type PublicAbono struct {
	ID            string    `json:"id"`
	Amount        float64   `json:"amount"`
	PaymentMethod string    `json:"payment_method"`
	CreatedAt     time.Time `json:"created_at"`
	ReceiptURL    string    `json:"receipt_url,omitempty"`
}

// PublicPaymentMethodLite mirrors PublicCatalog's shape so the
// live-tab page can reuse the same chip component.
type PublicPaymentMethodLite struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	Kind           string `json:"kind"`
	AccountDetails string `json:"account_details,omitempty"`
	PaymentLink    string `json:"payment_link,omitempty"`
	QRImageURL     string `json:"qr_image_url,omitempty"`
}

// GetPublicTableSession serves GET /api/v1/public/table-sessions/:session_token.
//
// Security posture:
//   - Lookup is by session_token only; we never expose the order
//     primary key or the tenant_id in the response.
//   - UUID-parse the token up front to reject trivially malformed
//     inputs without hitting the DB.
//   - Closed tickets (cobrado / cancelado) return 410 Gone so the
//     QR becomes useless after settlement — prevents forever-open
//     links from leaking past the meal.
func GetPublicTableSession(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimSpace(c.Param("session_token"))
		if _, err := uuid.Parse(token); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "sesión no encontrada"})
			return
		}

		var order models.OrderTicket
		err := db.Preload("Items").
			Where("session_token = ?", token).
			First(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "sesión no encontrada"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al cargar la cuenta",
				"detail": err.Error(),
			})
			return
		}

		if order.Status == models.OrderStatusCobrado ||
			order.Status == models.OrderStatusCancelado {
			c.JSON(http.StatusGone, gin.H{
				"error":  "la cuenta ya fue cerrada",
				"status": order.Status,
			})
			return
		}

		// Hydrate the tenant display fields. Missing tenant row is
		// unexpected (FK guarantees it exists) but we degrade to
		// empty strings rather than 500.
		var tenant models.Tenant
		_ = db.Select("business_name", "logo_url").
			Where("id = ?", order.TenantID).
			First(&tenant).Error

		items := make([]PublicTableItem, 0, len(order.Items))
		for _, it := range order.Items {
			items = append(items, PublicTableItem{
				ProductName: it.ProductName,
				Quantity:    it.Quantity,
				UnitPrice:   it.UnitPrice,
				Emoji:       it.Emoji,
				Subtotal:    it.UnitPrice * float64(it.Quantity),
				AddedAt:     it.CreatedAt,
			})
		}

		// Fetch approved abonos for this ticket so the public UI
		// can render "Abonos: $X" in green and the remaining.
		var abonos []models.PartialPayment
		db.Where("order_id = ? AND status = ? AND deleted_at IS NULL",
			order.ID, models.PartialPaymentStatusApproved).
			Order("created_at ASC").
			Find(&abonos)

		publicAbonos := make([]PublicAbono, 0, len(abonos))
		var paid float64
		for _, a := range abonos {
			paid += a.Amount
			publicAbonos = append(publicAbonos, PublicAbono{
				ID:            a.ID,
				Amount:        a.Amount,
				PaymentMethod: a.PaymentMethod,
				CreatedAt:     a.CreatedAt,
				ReceiptURL:    a.ReceiptURL,
			})
		}
		remaining := order.Total - paid
		if remaining < 0 {
			remaining = 0
		}

		// Tenant payment methods the customer can pick in the
		// "Hacer abono" modal. Active ones only, narrow projection.
		var methodRows []models.TenantPaymentMethod
		db.Where("tenant_id = ? AND is_active = true", order.TenantID).
			Order("created_at ASC").
			Find(&methodRows)
		publicMethods := make([]PublicPaymentMethodLite, 0, len(methodRows))
		for _, m := range methodRows {
			details := strings.TrimSpace(m.AccountDetails)
			kind := "wallet"
			link := ""
			switch {
			case strings.HasPrefix(details, "http://"),
				strings.HasPrefix(details, "https://"):
				link = details
				kind = "link"
			case m.Provider == "efectivo":
				kind = "cash"
			}
			publicMethods = append(publicMethods, PublicPaymentMethodLite{
				ID:             m.ID,
				Name:           m.Name,
				Provider:       m.Provider,
				Kind:           kind,
				AccountDetails: details,
				PaymentLink:    link,
				QRImageURL:     m.QRImageURL,
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"data": PublicTableSession{
				TableLabel:       order.Label,
				Status:           order.Status,
				Type:             order.Type,
				Total:            order.Total,
				CreatedAt:        order.CreatedAt,
				UpdatedAt:        order.UpdatedAt,
				WaiterCalledAt:   order.WaiterCalledAt,
				Items:            items,
				TenantName:       tenant.BusinessName,
				TenantBrandLogo:  tenant.LogoURL,
				PartialPayments:  publicAbonos,
				PaidAmount:       paid,
				RemainingBalance: remaining,
				PaymentMethods:   publicMethods,
			},
		})
	}
}

// SubmitPartialPayment serves POST /api/v1/public/table-sessions/:session_token/payments.
//
// Self-service abono from the public live-tab page. Creates the row
// in PENDING so the tendero has to confirm on the POS before it
// counts against the remaining balance — otherwise a customer could
// tap "Pagué $20.000 por Nequi" and walk out without actually
// paying. Approvals happen in a separate authenticated endpoint.
func SubmitPartialPayment(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Amount          float64 `json:"amount" binding:"required,gt=0"`
		PaymentMethod   string  `json:"payment_method"`
		PaymentMethodID string  `json:"payment_method_id"`
		Notes           string  `json:"notes"`
		// ReceiptURL is the URL of the screenshot the customer
		// uploaded via /receipts before this call. Optional —
		// cash abonos and intra-tienda transfers don't need a
		// receipt. We do NOT validate the URL host here so a
		// future migration to a different storage backend
		// doesn't require a backend redeploy in lockstep.
		ReceiptURL string `json:"receipt_url"`
	}

	return func(c *gin.Context) {
		token := strings.TrimSpace(c.Param("session_token"))
		if _, err := uuid.Parse(token); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "sesión no encontrada"})
			return
		}

		var order models.OrderTicket
		err := db.Where("session_token = ?", token).First(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "sesión no encontrada"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al cargar la cuenta",
				"detail": err.Error(),
			})
			return
		}
		if order.Status == models.OrderStatusCobrado ||
			order.Status == models.OrderStatusCancelado {
			c.JSON(http.StatusGone, gin.H{"error": "la cuenta ya fue cerrada"})
			return
		}

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		pmID := strings.TrimSpace(req.PaymentMethodID)
		if pmID != "" && !models.IsValidUUID(pmID) {
			pmID = ""
		}

		abono := models.PartialPayment{
			OrderID:         order.ID,
			TenantID:        order.TenantID,
			BranchID:        order.BranchID,
			Amount:          req.Amount,
			PaymentMethod:   strings.TrimSpace(req.PaymentMethod),
			PaymentMethodID: pmID,
			Status:          models.PartialPaymentStatusPending,
			Notes:           strings.TrimSpace(req.Notes),
			ReceiptURL:      strings.TrimSpace(req.ReceiptURL),
		}
		if err := db.Create(&abono).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "no pudimos registrar el abono",
				"detail": err.Error(),
			})
			return
		}

		// KDS bell for the tendero so they know to confirm it.
		label := order.Label
		if label == "" {
			label = "Mesa sin nombre"
		}
		CreateNotification(db, order.TenantID,
			"Cliente envió un abono",
			label+" registró un abono por confirmar",
			"partial_payment",
		)

		c.JSON(http.StatusCreated, gin.H{
			"data": gin.H{
				"id":     abono.ID,
				"status": abono.Status,
			},
		})
	}
}

// RegisterPartialPayment serves the authenticated POST for the
// tendero to record a manual abono from the POS (customer handed
// them cash). Creates the row directly as APPROVED so it counts
// against the remaining balance without an extra confirmation step.
func RegisterPartialPayment(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		OrderID         string  `json:"order_id" binding:"required"`
		Amount          float64 `json:"amount" binding:"required,gt=0"`
		PaymentMethod   string  `json:"payment_method"`
		PaymentMethodID string  `json:"payment_method_id"`
		Notes           string  `json:"notes"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if !models.IsValidUUID(req.OrderID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "order_id inválido"})
			return
		}

		var order models.OrderTicket
		if err := db.Where("id = ? AND tenant_id = ?", req.OrderID, tenantID).
			First(&order).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "orden no encontrada"})
			return
		}
		if order.Status == models.OrderStatusCobrado ||
			order.Status == models.OrderStatusCancelado {
			c.JSON(http.StatusGone, gin.H{"error": "la cuenta ya fue cerrada"})
			return
		}

		pmID := strings.TrimSpace(req.PaymentMethodID)
		if pmID != "" && !models.IsValidUUID(pmID) {
			pmID = ""
		}

		employeeID := middleware.GetUserID(c)
		var createdBy *string
		if models.IsValidUUID(employeeID) {
			createdBy = &employeeID
		}

		abono := models.PartialPayment{
			OrderID:           order.ID,
			TenantID:          tenantID,
			BranchID:          order.BranchID,
			Amount:            req.Amount,
			PaymentMethod:     strings.TrimSpace(req.PaymentMethod),
			PaymentMethodID:   pmID,
			Status:            models.PartialPaymentStatusApproved,
			Notes:             strings.TrimSpace(req.Notes),
			CreatedByEmployee: createdBy,
		}
		if err := db.Create(&abono).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "no pudimos registrar el abono",
				"detail": err.Error(),
			})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": abono})
	}
}

// ListPartialPayments serves GET /api/v1/orders/:uuid/partial-payments
// so the Flutter TabReviewScreen can render the abonos list without
// re-fetching the whole order. Scoped to the caller's tenant.
func ListPartialPayments(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		orderID := strings.TrimSpace(c.Param("uuid"))
		if !models.IsValidUUID(orderID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "uuid inválido"})
			return
		}

		// Confirm the order belongs to this tenant before listing —
		// prevents a crafted order_id from leaking cross-tenant
		// abonos.
		var exists int64
		db.Model(&models.OrderTicket{}).
			Where("id = ? AND tenant_id = ?", orderID, tenantID).
			Count(&exists)
		if exists == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "orden no encontrada"})
			return
		}

		var rows []models.PartialPayment
		db.Where("order_id = ? AND deleted_at IS NULL", orderID).
			Order("created_at ASC").
			Find(&rows)

		var paid float64
		for _, r := range rows {
			if r.Status == models.PartialPaymentStatusApproved {
				paid += r.Amount
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"payments":    rows,
				"paid_amount": paid,
			},
		})
	}
}

// CallWaiter serves POST /api/v1/public/table-sessions/:session_token/call-waiter.
//
// Idempotent by design: we rate-limit to one call per 60 s so a
// nervous customer tapping the button five times doesn't flood
// the KDS with notifications. Returns 200 on accept, 429 when
// suppressed so the client can show "ya avisaste hace unos
// segundos" instead of celebrating a silent no-op.
func CallWaiter(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimSpace(c.Param("session_token"))
		if _, err := uuid.Parse(token); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "sesión no encontrada"})
			return
		}

		var order models.OrderTicket
		err := db.Where("session_token = ?", token).First(&order).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "sesión no encontrada"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al procesar la llamada",
				"detail": err.Error(),
			})
			return
		}

		if order.Status == models.OrderStatusCobrado ||
			order.Status == models.OrderStatusCancelado {
			c.JSON(http.StatusGone, gin.H{"error": "la cuenta ya fue cerrada"})
			return
		}

		// Rate-limit: honor the last recorded ping.
		if order.WaiterCalledAt != nil && time.Since(*order.WaiterCalledAt) < time.Minute {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":        "ya avisaste al mesero hace unos segundos",
				"called_at":    order.WaiterCalledAt,
				"retry_after":  60 - int(time.Since(*order.WaiterCalledAt).Seconds()),
			})
			return
		}

		now := time.Now().UTC()
		if err := db.Model(&order).
			Update("waiter_called_at", now).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "no pudimos registrar la llamada",
				"detail": err.Error(),
			})
			return
		}

		// Push a notification into the tenant's activity feed so
		// the KDS bell lights up without the cashier needing to
		// refresh the orders screen. Non-fatal on failure — the
		// customer's UI already received the 200 OK.
		label := order.Label
		if label == "" {
			label = "Mesa sin nombre"
		}
		CreateNotification(db, order.TenantID,
			"Mesa llamando al mesero",
			label+" necesita atención",
			"waiter_call",
		)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"called_at": now,
			},
		})
	}
}

// UploadPaymentReceipt serves
//
//	POST /api/v1/public/table-sessions/:session_token/receipts
//
// Public — gated by the session_token. The customer uploads a
// screenshot of the transfer they just made, we persist it in the
// payment-receipts bucket, and return the public URL so the
// SubmitPartialPayment call can attach it.
//
// Security posture:
//   - Session token resolves the tenant; we never trust a query-string
//     tenant_id from a public request.
//   - Closed tickets (cobrado / cancelado) refuse the upload — a
//     long-lived QR shouldn't keep accepting receipts after the meal.
//   - 5 MiB hard cap, image/* enforced via Content-Type prefix.
//   - We do NOT store the receipt against the abono yet — the
//     client follows up with the abono POST that includes
//     receipt_url. Keeps the storage write idempotent and lets the
//     customer retry the upload without duplicating PartialPayment
//     rows.
func UploadPaymentReceipt(db *gorm.DB, storage services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimSpace(c.Param("session_token"))
		if _, err := uuid.Parse(token); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "sesión no encontrada"})
			return
		}

		if storage == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "servicio de almacenamiento no configurado",
			})
			return
		}

		var order models.OrderTicket
		if err := db.Where("session_token = ?", token).First(&order).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "sesión no encontrada"})
			return
		}
		if order.Status == models.OrderStatusCobrado ||
			order.Status == models.OrderStatusCancelado {
			c.JSON(http.StatusGone, gin.H{"error": "la cuenta ya fue cerrada"})
			return
		}

		file, header, err := c.Request.FormFile("receipt")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "archivo requerido (campo: receipt)",
			})
			return
		}
		defer file.Close()

		if header.Size <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "archivo vacío"})
			return
		}
		if header.Size > maxReceiptBytes {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "la imagen excede 5 MB",
			})
			return
		}

		mimeType := header.Header.Get("Content-Type")
		if mimeType == "" || !strings.HasPrefix(mimeType, "image/") {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "el archivo debe ser una imagen (PNG/JPEG)",
			})
			return
		}

		data, err := io.ReadAll(io.LimitReader(file, maxReceiptBytes+1))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al leer imagen",
				"detail": err.Error(),
			})
			return
		}
		if len(data) > maxReceiptBytes {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "la imagen excede 5 MB",
			})
			return
		}

		ext := extFromMime(mimeType)
		// Tenant-prefixed key keeps receipts naturally isolated per
		// merchant and makes future bucket-level retention rules
		// easy to scope. Order id provides traceability when ops
		// audits a disputed transfer.
		key := fmt.Sprintf("%s/%s/%s%s",
			order.TenantID, order.ID, uuid.NewString(), ext)

		publicURL, err := storage.Upload(
			c.Request.Context(), paymentReceiptBucket, key, data, mimeType)
		if err != nil {
			log.Printf("[RECEIPTS] upload failed tenant=%s order=%s bytes=%d: %v",
				order.TenantID, order.ID, len(data), err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "no se pudo subir el comprobante",
				"detail": err.Error(),
			})
			return
		}

		c.JSON(http.StatusCreated, gin.H{
			"data": gin.H{
				"receipt_url": publicURL,
			},
		})
	}
}
