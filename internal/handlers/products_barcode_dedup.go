// Spec: specs/100-completar-skus-inventario/spec.md
//
// Spec 100 / D1 — dedup de barcode por tenant. Un código de barras identifica
// UN producto dentro de la tienda: si dos referencias comparten código, el
// POS cobra el producto equivocado (Art. VII). Este archivo concentra la
// lógica compartida entre CreateProduct y UpdateProduct: el lookup del
// producto dueño, la respuesta 409 `duplicate_barcode` y el clasificador de
// la violación del índice único parcial (la barrera total contra la carrera
// de dos requests simultáneos que pasaron el pre-check).
package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// productBarcodeUniqueIndex es el índice único parcial (tenant_id, barcode)
// creado en el bootstrap (database.applyProductBarcodeIndex). El nombre se
// usa para reconocer su violación en el error que devuelve GORM.
const productBarcodeUniqueIndex = "idx_products_tenant_barcode_unique"

// findBarcodeOwner devuelve el producto VIVO del tenant que ya usa `barcode`,
// excluyendo `excludeID` (el propio producto al re-guardar su código). nil =
// código libre. El scope por tenant es obligatorio (Art. III) y el soft-delete
// lo aplica GORM por defecto. Un error real de BD se loguea y se trata como
// "libre": el índice único del bootstrap es la barrera final.
func findBarcodeOwner(db *gorm.DB, tenantID, barcode, excludeID string) *models.Product {
	if barcode == "" {
		return nil
	}
	var owner models.Product
	err := db.Where("tenant_id = ? AND barcode = ? AND id != ?",
		tenantID, barcode, excludeID).First(&owner).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("[products] barcode owner lookup failed (tenant=%s): %v", tenantID, err)
		}
		return nil
	}
	return &owner
}

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

// IsProductBarcodeUniqueViolation detecta la violación del índice único
// parcial de barcode (SQLSTATE 23505 sobre idx_products_tenant_barcode_
// unique) — la carrera pura de dos requests que pasaron el pre-check. Mismo
// criterio string-match de isUniqueViolation (fiado.go): evita pgconn como
// dependencia directa y acota al índice concreto para no atrapar otros
// duplicate-key (p. ej. products_pkey, que tiene su propio manejo).
func IsProductBarcodeUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), productBarcodeUniqueIndex)
}
