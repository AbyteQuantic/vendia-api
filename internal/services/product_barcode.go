// Spec: specs/100-completar-skus-inventario/spec.md
//
// Spec 100 / D1 — dedup de barcode por tenant. Un código de barras identifica
// UN producto dentro de la tienda: si dos referencias comparten código, el
// POS cobra el producto equivocado (Art. VII). Estos helpers viven en
// services (no en handlers) porque los usan TODOS los caminos de escritura
// de barcode: CreateProduct/UpdateProduct (handlers/products.go), el
// importador CSV (handlers/products_import.go) y el sync offline
// (services/sync_service.go) — handlers importa services, nunca al revés.
package services

import (
	"errors"
	"log"
	"strings"

	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// productBarcodeUniqueIndex es el índice único parcial (tenant_id, barcode)
// creado en el bootstrap (database.applyProductBarcodeIndex). El nombre se
// usa para reconocer su violación en el error que devuelve GORM.
const productBarcodeUniqueIndex = "idx_products_tenant_barcode_unique"

// FindBarcodeOwner devuelve el producto VIVO del tenant que ya usa `barcode`,
// excluyendo `excludeID` (el propio producto al re-guardar su código). nil =
// código libre. El scope por tenant es obligatorio (Art. III) y el soft-delete
// lo aplica GORM por defecto. Un error real de BD se loguea y se trata como
// "libre": el índice único del bootstrap es la barrera final.
func FindBarcodeOwner(db *gorm.DB, tenantID, barcode, excludeID string) *models.Product {
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

// IsProductBarcodeUniqueViolation detecta la violación del índice único
// parcial de barcode (SQLSTATE 23505 sobre idx_products_tenant_barcode_
// unique) — la carrera pura de dos escrituras que pasaron el pre-check.
// Mismo criterio string-match de isUniqueViolation (handlers/fiado.go):
// evita pgconn como dependencia directa y acota al índice concreto para no
// atrapar otros duplicate-key (p. ej. products_pkey, que tiene su propio
// manejo idempotente).
func IsProductBarcodeUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), productBarcodeUniqueIndex)
}
