package models

type Supplier struct {
	BaseModel

	TenantID    string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	CompanyName string `gorm:"not null" json:"company_name"`
	ContactName string `json:"contact_name,omitempty"`
	Phone       string `gorm:"not null" json:"phone"`
	Emoji       string `json:"emoji,omitempty"`

	// Spec 081 — ubicación OPCIONAL del proveedor para el mapa "Mercado cercano".
	// Aditivo (Art. X). (0,0) = sin ubicación → no sale en el mapa. El tendero la
	// fija al crear/editar el proveedor (tocar en el mapa). Dirección legible.
	Latitude  float64 `gorm:"default:0" json:"latitude"`
	Longitude float64 `gorm:"default:0" json:"longitude"`
	Address   string  `gorm:"type:text;default:''" json:"address,omitempty"`
}
