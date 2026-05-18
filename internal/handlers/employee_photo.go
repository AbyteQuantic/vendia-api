// Spec: specs/019-foto-perfil-tendero-empleado/spec.md
package handlers

import (
	"fmt"
	"io"
	"log"
	"net/http"

	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// profilePhotoBucket is the Supabase/R2 bucket where the profile photos
// of the tendero (dueño) and every employee land. The StorageService
// auto-creates the bucket (public, idempotent) on first upload, so no
// manual provisioning is needed.
const profilePhotoBucket = "profile-photos"

// maxProfilePhotoBytes caps the upload at 5 MiB. A normalized selfie
// from the Flutter client is far smaller; the cap is a cheap defense
// against a crafted oversized request.
const maxProfilePhotoBytes = 5 << 20

// profilePhotoExt maps a sniffed image MIME to a safe file extension
// for the storage key. Only the formats the bucket accepts appear here
// because detectImageType + uploadableImageTypes already gate the
// upload before this is called.
var profilePhotoExt = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// UploadEmployeePhoto stores a profile photo for an employee — or the
// owner, who is just an Employee row with is_owner=true (Feature 019,
// D1). The image is pushed to the profile-photos bucket and its public
// URL persisted on Employee.photo_url, then echoed back so the
// frontend can render the avatar immediately.
//
// Contract:
//
//	POST /api/v1/store/employees/:uuid/photo
//	Content-Type: multipart/form-data
//	Form fields:
//	  photo  (required, file ≤ 5 MiB, jpeg/png/webp/gif)
//	200: {data: <employee>}   — includes photo_url
//	400: photo missing / empty / unsupported format (e.g. HEIC)
//	404: employee not found in this tenant (Constitution Art. III)
//	500: storage / persistence failure (detail carries the raw cause)
//	503: storage backend not configured
//
// Multi-tenant isolation (Constitution Art. III): the employee is
// looked up scoped to the JWT's tenant_id BEFORE any storage write, so
// a crafted uuid targeting another tenant's staff is rejected with a
// 404 and no upload happens.
//
// Image format validation reuses detectImageType / uploadableImageTypes
// from Feature 010: the real bytes are sniffed (the client
// Content-Type is never trusted) and HEIC is rejected with a clear
// Spanish 400 instead of a generic storage 500.
func UploadEmployeePhoto(db *gorm.DB, storage services.FileStorage) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)
		employeeID := c.Param("uuid")

		if storage == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "servicio de almacenamiento no configurado",
			})
			return
		}

		// Tenant-scoped lookup first — refuses uploads to another
		// tenant's employee even if the uuid is guessed.
		var employee models.Employee
		if err := db.Where("id = ? AND tenant_id = ?", employeeID, tenantID).
			First(&employee).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "empleado no encontrado",
			})
			return
		}

		file, header, err := c.Request.FormFile("photo")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "foto requerida (campo: photo)",
			})
			return
		}
		defer file.Close()

		if header.Size <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "archivo vacío"})
			return
		}
		if header.Size > maxProfilePhotoBytes {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "la foto excede 5 MB",
			})
			return
		}

		data, err := io.ReadAll(io.LimitReader(file, maxProfilePhotoBytes+1))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "error al leer la foto",
				"detail": err.Error(),
			})
			return
		}
		// LimitReader went one byte over → real size exceeded the cap.
		if len(data) > maxProfilePhotoBytes {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "la foto excede 5 MB",
			})
			return
		}

		// Feature 010: never trust the client Content-Type. Sniff the
		// real bytes; reject HEIC (and anything the bucket can't store)
		// with a clear Spanish 400 instead of a generic upstream 500.
		mimeType := detectImageType(data)
		if !uploadableImageTypes[mimeType] {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      logoFormatoNoSoportadoMsg,
				"error_code": logoFormatoNoSoportadoCode,
			})
			return
		}

		// Namespace the key by tenant + employee so a re-upload
		// overwrites cleanly and two tenants never collide.
		key := fmt.Sprintf("%s/%s-%s%s",
			tenantID, employee.ID, uuid.NewString()[:8], profilePhotoExt[mimeType])

		photoURL, err := storage.Upload(
			c.Request.Context(), profilePhotoBucket, key, data, mimeType)
		if err != nil {
			log.Printf("[EMPLOYEE_PHOTO] upload failed tenant=%s id=%s bytes=%d: %v",
				tenantID, employeeID, len(data), err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "no se pudo subir la foto",
				"detail": err.Error(),
			})
			return
		}

		if err := db.Model(&employee).
			Update("photo_url", photoURL).Error; err != nil {
			log.Printf("[EMPLOYEE_PHOTO] persist url failed tenant=%s id=%s: %v",
				tenantID, employeeID, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "no se pudo guardar la foto",
				"detail": err.Error(),
			})
			return
		}
		employee.PhotoURL = photoURL

		c.JSON(http.StatusOK, gin.H{"data": employee})
	}
}
