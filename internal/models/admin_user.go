package models

type AdminUser struct {
	BaseModel

	Email        string `gorm:"not null;uniqueIndex" json:"email"`
	PasswordHash string `gorm:"not null" json:"-"`
	Name         string `gorm:"not null" json:"name"`
	IsSuperAdmin bool   `gorm:"default:true" json:"is_super_admin"`
}
