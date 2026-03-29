package handlers

import (
	"io"
	"net/http"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
)

func OCRInvoice(geminiAPIKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if geminiAPIKey == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OCR no configurado"})
			return
		}

		file, header, err := c.Request.FormFile("image")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "imagen requerida (campo: image)"})
			return
		}
		defer file.Close()

		if header.Size > 5<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "imagen excede 5MB"})
			return
		}

		mimeType := header.Header.Get("Content-Type")
		if mimeType != "image/jpeg" && mimeType != "image/png" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "solo se aceptan JPEG y PNG"})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al leer imagen"})
			return
		}

		svc := services.NewOCRService(geminiAPIKey)
		result, err := svc.ProcessInvoice(data, mimeType)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": result})
	}
}
