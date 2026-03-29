package models

type RockolaSuggestion struct {
	BaseModel

	TenantID   string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	TrackName  string `gorm:"not null" json:"track_name"`
	ArtistName string `gorm:"not null" json:"artist_name"`
	ArtworkURL string `json:"artwork_url,omitempty"`
	Status     string `gorm:"default:'pending'" json:"status"`
	SuggestedBy string `json:"suggested_by,omitempty"`
}
