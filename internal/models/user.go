package models

type User struct {
	BaseModel

	Phone        string `gorm:"not null;uniqueIndex" json:"phone"`
	Name         string `gorm:"not null;default:''" json:"name"`
	PasswordHash string `gorm:"not null;default:''" json:"-"`

	Workspaces []UserWorkspace `gorm:"foreignKey:UserID" json:"workspaces,omitempty"`
}
