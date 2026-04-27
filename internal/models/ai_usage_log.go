package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AIUsageLog stores one Gemini call's token usage and estimated USD cost
// for super-admin FinOps (per-tenant attribution).
type AIUsageLog struct {
	ID               string  `gorm:"type:uuid;primaryKey" json:"id"`
	TenantID         string  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	Feature          string  `gorm:"type:varchar(32);not null;index" json:"feature"`
	TokensInput      int64   `gorm:"not null;default:0" json:"tokens_input"`
	TokensOutput     int64   `gorm:"not null;default:0" json:"tokens_output"`
	EstimatedCostUSD float64 `gorm:"type:double precision;not null;default:0" json:"estimated_cost_usd"`
	ModelName        string  `gorm:"type:varchar(128);not null;default:''" json:"model_name"`
	CreatedAt        time.Time `gorm:"not null;index" json:"created_at"`
}

func (AIUsageLog) TableName() string { return "ai_usage_logs" }

func (a *AIUsageLog) BeforeCreate(tx *gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	return nil
}
