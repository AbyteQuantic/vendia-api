// Spec: specs/084-peluqueria-salon/spec.md (backlog #2 — asistencia para arriendo)
package models

import "time"

// StaffAttendance — registro de que un profesional ASISTIÓ un día. Sirve para
// cobrar el arriendo de silla solo por los días presentes (no por días
// calendario). Una fila por (tenant, empleado, día). Date en 'YYYY-MM-DD' hora
// Colombia para que el índice único sea día-granular sin problemas de zona.
type StaffAttendance struct {
	BaseModel

	TenantID     string  `gorm:"type:uuid;index;not null" json:"tenant_id"`
	BranchID     *string `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	EmployeeUUID string  `gorm:"type:uuid;index;not null" json:"employee_uuid"`
	Date         string  `gorm:"type:varchar(10);not null;index" json:"date"`

	// Spec 105 F5 — horas del día: el primer verify-pin del día estampa
	// ClockIn; el empleado (o el dueño) cierra con clock-out. Aditivos:
	// las filas de asistencia pre-F5 (arriendo de silla Spec 084) siguen
	// válidas con ambos en null.
	ClockIn  *time.Time `json:"clock_in,omitempty"`
	ClockOut *time.Time `json:"clock_out,omitempty"`
}
