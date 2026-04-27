package models

// AIFeature values are stored in ai_usage_logs.feature for FinOps dashboards.
// Keep stable — the admin UI maps them to Spanish labels.
const (
	AIFeatureVoiceInv     = "VOICE_INV"
	AIFeatureOCRInvoice   = "OCR_INVOICE"
	AIFeatureChatIA       = "CHAT_IA"
	AIFeaturePromoBanner  = "PROMO_BANNER"
	AIFeatureEnhancePhoto = "ENHANCE_PHOTO"
	AIFeatureProductImage = "PRODUCT_IMAGE"
	AIFeatureLogoGen      = "LOGO_GEN"
)
