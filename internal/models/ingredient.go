// Spec: specs/001-insumos-recetas/spec.md
package models

import "time"

// Unit is the fixed enum of measurement units an ingredient can use.
// Spec D5: {unidad, g, kg, ml, l}. The recipe quantity is always
// expressed in the unit of the ingredient it consumes.
const (
	UnitUnidad = "unidad"
	UnitG      = "g"
	UnitKg     = "kg"
	UnitML     = "ml"
	UnitL      = "l"
)

// validUnits is the source of truth for unit validation. Kept private
// so callers go through IsValidUnit / NormalizeUnit.
var validUnits = map[string]bool{
	UnitUnidad: true,
	UnitG:      true,
	UnitKg:     true,
	UnitML:     true,
	UnitL:      true,
}

// IsValidUnit reports whether s is one of the fixed measurement units.
func IsValidUnit(s string) bool {
	return validUnits[s]
}

// NormalizeUnit returns a safe unit value: a valid unit is returned
// as-is, anything else (empty or unknown) falls back to "unidad" — the
// sensible default (Art. I, Cero Fricción: never block on a bad enum).
func NormalizeUnit(s string) string {
	if IsValidUnit(s) {
		return s
	}
	return UnitUnidad
}

// Ingredient (insumo) is raw-material inventory, distinct from a
// vendible Product. Its stock is mutated ONLY through kardex
// movements (Spec §7), never written directly. Multi-tenant: every
// query filters by TenantID (Art. III).
type Ingredient struct {
	BaseModel

	TenantID   string     `gorm:"type:uuid;not null;index" json:"tenant_id"`
	Name       string     `gorm:"not null" json:"name"`
	Unit       string     `gorm:"type:varchar(16);default:'unidad'" json:"unit"`
	Stock      float64    `gorm:"default:0" json:"stock"`
	MinStock   float64    `gorm:"default:0" json:"min_stock"`
	UnitCost   float64    `gorm:"default:0" json:"unit_cost"`
	ExpiryDate *time.Time `json:"expiry_date,omitempty"`
	SupplierID *string    `gorm:"type:uuid" json:"supplier_id,omitempty"`
}

// IsLowStock reports whether the ingredient sits at or below its
// minimum threshold (AC-05). An ingredient with MinStock 0 is never
// considered low — there is no threshold to breach.
func (i Ingredient) IsLowStock() bool {
	if i.MinStock <= 0 {
		return false
	}
	return i.Stock < i.MinStock
}
