// Spec: specs/084-peluqueria-salon/spec.md
//
// Acceso a datos para congelar la comisión de una venta/cierre de comanda. La
// MATEMÁTICA de comisión (tasa/monto/prorrateo) vive en payroll_service.go, que
// es pura y sin BD; aquí solo está lo que sí toca la BD: resolver la config de
// pago ACTIVA del profesional, con caché por operación para no consultar por
// cada línea del mismo empleado. Extraído de CreateSale y CloseOrder, que tenían
// esta resolución duplicada línea por línea.
package services

import (
	"vendia-backend/internal/models"

	"gorm.io/gorm"
)

// CommissionResolver resuelve y cachea la EmployeePayConfig activa de cada
// profesional dentro de UNA operación (una venta o un cierre de comanda). Se
// construye con la *gorm.DB en curso —normalmente la transacción del handler— y
// el tenant; su caché NO es segura para uso concurrente ni para reutilizar entre
// requests (igual que las closures que reemplaza: su vida es una sola operación).
type CommissionResolver struct {
	db       *gorm.DB
	tenantID string
	cache    map[string]*models.EmployeePayConfig
}

// NewCommissionResolver crea un resolver ligado a la transacción/DB y al tenant
// de la operación en curso.
func NewCommissionResolver(db *gorm.DB, tenantID string) *CommissionResolver {
	return &CommissionResolver{
		db:       db,
		tenantID: tenantID,
		cache:    map[string]*models.EmployeePayConfig{},
	}
}

// Config devuelve la config de pago ACTIVA del empleado (la más reciente por
// effective_from) o nil si no tiene una. Cachea el resultado —incluido el nil—
// para que un profesional que aparece en varias líneas se consulte una sola vez.
func (r *CommissionResolver) Config(employeeUUID string) *models.EmployeePayConfig {
	if v, ok := r.cache[employeeUUID]; ok {
		return v
	}
	var cfg models.EmployeePayConfig
	if err := r.db.Where("tenant_id = ? AND employee_uuid = ? AND is_active = ?",
		r.tenantID, employeeUUID, true).Order("effective_from DESC").First(&cfg).Error; err != nil {
		r.cache[employeeUUID] = nil
		return nil
	}
	r.cache[employeeUUID] = &cfg
	return &cfg
}

// RecomputeCommissionOnNetOfTax reajusta el monto de comisión CONGELADO de cada
// línea con PayBasis=commission para que la base sea el NET (subtotal − IVA
// prorrateado por línea con largest-remainder) en vez del gross. Así el
// profesional no cobra comisión sobre el impuesto (Spec 084, backlog #4). La
// comisión ya viene congelada sobre el subtotal (gross) desde el loop del
// handler; esto solo la corrige cuando la venta lleva IVA. Muta `items` in-place.
// Si taxAmount <= 0 (tenants exentos) es un no-op.
func RecomputeCommissionOnNetOfTax(items []models.SaleItem, taxAmount float64) {
	if taxAmount <= 0 {
		return
	}
	weights := make([]float64, len(items))
	for i := range items {
		weights[i] = items[i].Subtotal
	}
	taxParts := ProrateLargestRemainder(taxAmount, weights)
	for i := range items {
		if items[i].PayBasis == models.PayBasisCommission && items[i].CommissionPct != nil {
			net := items[i].Subtotal - taxParts[i]
			items[i].CommissionAmount =
				float64(int64(net*(*items[i].CommissionPct)/100.0 + 0.5))
		}
	}
}
