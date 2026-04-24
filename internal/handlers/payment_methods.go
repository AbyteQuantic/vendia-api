package handlers

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// paymentQRBucket is the Supabase/R2 bucket name where tenders upload
// QR screenshots for their Nequi / Daviplata / Bancolombia / Breve
// accounts. We keep it isolated from `promo-banners` and
// `vendia-logos` so ACL changes on one don't accidentally leak into
// the other.
const paymentQRBucket = "payment-qrs"

// maxQRBytes caps uploads at 3 MiB. QR screenshots from Colombian
// banking apps hover around 300-800 KB; 3 MiB is a generous ceiling
// that still protects the storage quota from a rogue gallery pick.
const maxQRBytes = 3 << 20

func ListPaymentMethods(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var methods []models.TenantPaymentMethod
		if err := db.Where("tenant_id = ?", tenantID).
			Order("created_at ASC").
			Find(&methods).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al obtener métodos de pago"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": methods, "count": len(methods)})
	}
}

func CreatePaymentMethod(db *gorm.DB) gin.HandlerFunc {
	// Request adds an optional `provider` field on top of the original
	// schema. Existing clients that only send {name, account_details}
	// keep working — the server derives `provider` by lowercasing
	// `name` as a best-effort fallback.
	type Request struct {
		ID             string `json:"id"`
		Name           string `json:"name" binding:"required"`
		AccountDetails string `json:"account_details"`
		Provider       string `json:"provider"`
		QRImageURL     string `json:"qr_image_url"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		provider := strings.ToLower(strings.TrimSpace(req.Provider))
		if provider == "" {
			// Fall back to the name so list views still group by
			// wallet even for pre-provider payloads.
			provider = normalizeProviderFromName(req.Name)
		}

		pm := models.TenantPaymentMethod{
			TenantID:       tenantID,
			Name:           req.Name,
			AccountDetails: req.AccountDetails,
			Provider:       provider,
			QRImageURL:     strings.TrimSpace(req.QRImageURL),
			IsActive:       true,
		}
		if req.ID != "" && models.IsValidUUID(req.ID) {
			pm.ID = req.ID
		}

		if err := db.Create(&pm).Error; err != nil {
			log.Printf("[PAYMENT_METHODS] create failed tenant=%s: %v",
				tenantID, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al crear método de pago",
				"detail": err.Error(),
			})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": pm})
	}
}

// normalizeProviderFromName turns a free-form method name ("Nequi",
// "NEQUI 3001234567", "Bancolombia a la Mano") into the canonical
// provider id used by the public catalog ("nequi", "bancolombia").
// Kept here (not in a shared util) because it's only meaningful in
// the payment-methods context.
func normalizeProviderFromName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(n, "nequi"):
		return "nequi"
	case strings.Contains(n, "daviplata"):
		return "daviplata"
	case strings.Contains(n, "bancolombia"):
		return "bancolombia"
	case strings.Contains(n, "davivienda"):
		return "davivienda"
	case strings.Contains(n, "breve"):
		return "breve"
	case strings.Contains(n, "efectivo"), strings.Contains(n, "cash"):
		return "efectivo"
	case strings.Contains(n, "tarjeta"), strings.Contains(n, "card"):
		return "tarjeta"
	default:
		return "otro"
	}
}

func UpdatePaymentMethod(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		Name           *string `json:"name"`
		AccountDetails *string `json:"account_details"`
		IsActive       *bool   `json:"is_active"`
		Provider       *string `json:"provider"`
		QRImageURL     *string `json:"qr_image_url"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		pmID := c.Param("id")

		var pm models.TenantPaymentMethod
		if err := db.Where("id = ? AND tenant_id = ?", pmID, tenantID).
			First(&pm).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "método de pago no encontrado"})
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
		if req.AccountDetails != nil {
			updates["account_details"] = *req.AccountDetails
		}
		if req.IsActive != nil {
			updates["is_active"] = *req.IsActive
		}
		if req.Provider != nil {
			updates["provider"] = strings.ToLower(strings.TrimSpace(*req.Provider))
		}
		if req.QRImageURL != nil {
			updates["qr_image_url"] = strings.TrimSpace(*req.QRImageURL)
		}

		if err := db.Model(&pm).Updates(updates).Error; err != nil {
			log.Printf("[PAYMENT_METHODS] update failed tenant=%s id=%s: %v",
				tenantID, pmID, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al actualizar método de pago",
				"detail": err.Error(),
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": pm})
	}
}

// UploadPaymentMethodQR accepts a multipart form upload (field name
// `qr`) for a specific payment method, persists the image in the
// configured storage (Supabase / R2) and writes the public URL back
// into payment_methods.qr_image_url.
//
// Contract:
//
//	POST /api/v1/store/payment-methods/:id/qr
//	Content-Type: multipart/form-data
//	Form fields:
//	  qr   (required, file ≤ 3 MiB, image/*)
//
// Error surface mirrors what we already do for promotions and logo
// uploads: generic user-facing message + `detail` so the tendero's
// toast reveals the upstream storage failure without cracking open
// Render logs.
func UploadPaymentMethodQR(db *gorm.DB, storage services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		pmID := c.Param("id")

		if storage == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "servicio de almacenamiento no configurado",
			})
			return
		}

		// Tenant-scoped lookup first — refuses uploads to other
		// tenants' methods even if the id is guessed.
		var pm models.TenantPaymentMethod
		if err := db.Where("id = ? AND tenant_id = ?", pmID, tenantID).
			First(&pm).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "método de pago no encontrado",
			})
			return
		}

		file, header, err := c.Request.FormFile("qr")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "archivo QR requerido (campo: qr)",
			})
			return
		}
		defer file.Close()

		if header.Size <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "archivo vacío"})
			return
		}
		if header.Size > maxQRBytes {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "la imagen excede 3 MB",
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

		data, err := io.ReadAll(io.LimitReader(file, maxQRBytes+1))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al leer imagen",
				"detail": err.Error(),
			})
			return
		}
		// LimitReader went one byte over → actual size exceeded cap.
		if len(data) > maxQRBytes {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "la imagen excede 3 MB",
			})
			return
		}

		ext := extFromMime(mimeType)
		key := fmt.Sprintf("%s/%s-%s%s",
			tenantID, pm.ID, uuid.NewString()[:8], ext)

		publicURL, err := storage.Upload(
			c.Request.Context(), paymentQRBucket, key, data, mimeType)
		if err != nil {
			log.Printf("[PAYMENT_METHODS] QR upload failed tenant=%s id=%s bytes=%d: %v",
				tenantID, pmID, len(data), err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "no se pudo subir el QR",
				"detail": err.Error(),
			})
			return
		}

		if err := db.Model(&pm).
			Update("qr_image_url", publicURL).Error; err != nil {
			log.Printf("[PAYMENT_METHODS] persist QR url failed tenant=%s id=%s: %v",
				tenantID, pmID, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "no se pudo guardar el QR",
				"detail": err.Error(),
			})
			return
		}
		pm.QRImageURL = publicURL

		c.JSON(http.StatusOK, gin.H{"data": pm})
	}
}

// extFromMime picks a safe file extension from the Content-Type the
// client claims. Defaults to .png because that's what Colombian
// banking apps emit when you "Share QR → Save as image".
func extFromMime(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/png":
		return ".png"
	default:
		return ".png"
	}
}

func DeletePaymentMethod(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		pmID := c.Param("id")

		result := db.Where("id = ? AND tenant_id = ?", pmID, tenantID).
			Delete(&models.TenantPaymentMethod{})
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "método de pago no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "método de pago eliminado"})
	}
}
