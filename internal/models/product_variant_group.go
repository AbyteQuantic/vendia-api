// Spec: specs/095-variantes-producto/spec.md
package models

// ProductVariantGroup agrupa Products que son la MISMA prenda/ítem en
// presentaciones distintas (talla, color) — usado únicamente para armar la
// tarjeta agrupada del catálogo público, el selector del POS, y la fila
// colapsable de Mi Inventario. NUNCA reemplaza al Product de cada variante:
// stock/precio/kardex/venta siguen siendo del Product puntual (Art. VII).
//
// Sin BranchID propio a propósito: el grupo se deriva de las sedes de sus
// variantes vivas, para no divergir entre grupo y variante (auditoría del
// concilio de diseño, riesgo multi-sede).
type ProductVariantGroup struct {
	BaseModel
	TenantID string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	Name     string `gorm:"not null" json:"name"`
	Category string `json:"category"`
	ImageURL string `json:"image_url"`

	// AttributeLabels — ej. ["Talla","Color"]: define qué atributos arman
	// el selector del comprador y en qué orden. JSON array serializado.
	AttributeLabels string `gorm:"type:jsonb;not null;default:'[]'" json:"attribute_labels"`
}
