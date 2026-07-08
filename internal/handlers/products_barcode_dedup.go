// Spec: specs/100-completar-skus-inventario/spec.md
//
// Spec 100 / D1 — dedup de barcode por tenant. La lógica compartida (lookup
// del producto dueño + clasificador de la violación del índice único) vive
// en services/product_barcode.go porque también la usan el importador CSV y
// el sync offline. Aquí queda solo la pieza HTTP: la respuesta 409
// `duplicate_barcode` que comparten CreateProduct y UpdateProduct.
package handlers

import (
	"net/http"

	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
)

// respondDuplicateBarcode responde el 409 con el producto dueño del código.
// Contrato (plan 100 §4): {"error":"duplicate_barcode","message":"<español>",
// "existing_product":{"id","name","presentation"}}. image_url viaja además
// para que la tarjeta de conflicto del frontend muestre la foto sin otra
// vuelta al servidor. owner nil (carrera donde el dueño ya no es legible)
// degrada a un 409 sin existing_product — nunca un 500.
func respondDuplicateBarcode(c *gin.Context, owner *models.Product) {
	if owner == nil {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "duplicate_barcode",
			"message": "Ese código de barras ya está asignado a otro producto.",
		})
		return
	}
	c.JSON(http.StatusConflict, gin.H{
		"error":   "duplicate_barcode",
		"message": "Ese código de barras ya está asignado a \"" + owner.Name + "\".",
		"existing_product": gin.H{
			"id":           owner.ID,
			"name":         owner.Name,
			"presentation": owner.Presentation,
			"image_url":    owner.ImageURL,
		},
	})
}
