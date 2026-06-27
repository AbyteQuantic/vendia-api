// Spec: specs/084-peluqueria-salon/spec.md (backlog #2 — asistencia para arriendo)
package models

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
}
