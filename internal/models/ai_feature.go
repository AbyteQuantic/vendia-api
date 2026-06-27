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
	// Spec F042 — diseño de escarapela y certificado de eventos con IA.
	AIFeatureEventBadge = "EVENT_BADGE"
	AIFeatureEventCert  = "EVENT_CERT"
	// Spec F045 — extracción de campos del onboarding desde texto/voz.
	AIFeatureOnboarding = "ONBOARDING_PARSE"
	// Spec 065 — Recipe Studio: dictado de receta por voz y asistente IA
	// (completar / refinar ingredientes y pasos).
	AIFeatureVoiceRecipe  = "VOICE_RECIPE"
	AIFeatureRecipeAssist = "RECIPE_ASSIST"
	// Spec 085 — vender por voz: comandos de venta dictados (POS).
	AIFeatureVoiceOrder = "VOICE_ORDER"
)
