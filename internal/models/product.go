package models

type Product struct {
	BaseModel

	TenantID          string  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	CreatedBy         *string `gorm:"type:uuid;index" json:"created_by,omitempty"`
	BranchID          *string `gorm:"type:uuid;index" json:"branch_id,omitempty"`
	Name              string  `gorm:"not null" json:"name"`
	Price             float64 `gorm:"not null" json:"price"`
	PurchasePrice     float64 `gorm:"default:0" json:"purchase_price"`
	Stock             int     `gorm:"default:0" json:"stock"`
	MinStock          int     `gorm:"default:0" json:"min_stock"`
	Barcode           string  `gorm:"index" json:"barcode,omitempty"`
	CategoryID        *string `gorm:"type:uuid" json:"category_id,omitempty"`
	Category          string  `json:"category,omitempty"`
	Emoji             string  `json:"emoji,omitempty"`
	ImageURL          string  `json:"image_url,omitempty"`
	PhotoURL          string  `json:"photo_url,omitempty"`
	IsAvailable       bool    `gorm:"default:true" json:"is_available"`
	RequiresContainer bool    `gorm:"default:false" json:"requires_container"`
	ContainerPrice    int64   `gorm:"default:0" json:"container_price"`
	ExpiryDate        *string `json:"expiry_date,omitempty"`
	IngestionMethod   string  `gorm:"default:'manual'" json:"ingestion_method"`
	PriceStatus       string  `gorm:"default:'set'" json:"price_status"`
	SupplierID        *string `gorm:"type:uuid" json:"supplier_id,omitempty"`
	Unit              string  `gorm:"default:'unit'" json:"unit"`
	Presentation      string  `json:"presentation,omitempty"`  // botella, lata, bolsa, caja, etc.
	Content           string  `json:"content,omitempty"`       // 350ml, 500g, 1L, etc.
	IsAIEnhanced      bool    `gorm:"default:false" json:"is_ai_enhanced"`
}
