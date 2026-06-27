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
	ExpiryDate        *string `gorm:"type:date" json:"expiry_date,omitempty"`
	IngestionMethod   string  `gorm:"default:'manual'" json:"ingestion_method"`
	PriceStatus       string  `gorm:"default:'set'" json:"price_status"`
	SupplierID        *string `gorm:"type:uuid" json:"supplier_id,omitempty"`
	Unit              string  `gorm:"default:'unit'" json:"unit"`
	Presentation      string  `json:"presentation,omitempty"` // botella, lata, bolsa, caja, etc.
	Content           string  `json:"content,omitempty"`      // 350ml, 500g, 1L, etc.
	IsAIEnhanced      bool    `gorm:"default:false" json:"is_ai_enhanced"`

	// Feature 001 — a product is either "directo" (default) or
	// "receta". When IsRecipe is true, selling it explodes RecipeID's
	// recipe and discounts the ingredients instead of the product's
	// own stock. Additive + default false: every existing product
	// stays a direct product and its sale path is untouched (AC-06).
	IsRecipe bool    `gorm:"default:false" json:"is_recipe"`
	RecipeID *string `gorm:"type:uuid;index" json:"recipe_id,omitempty"`

	// ── Spec 080 — platos por porciones (pre-hechos del día) ────────────
	// AvailabilityMode: 'a_demanda' (default — se prepara al pedirlo, la venta
	// explota la receta y descuenta insumos) | 'por_porciones' (se cocina un
	// lote en la mañana: los insumos se descuentan UNA vez al preparar, y cada
	// venta descuenta `stock` —porciones restantes— sin re-explotar la receta).
	// Aditivo + default ''/a_demanda → los platos existentes no cambian (Art. X).
	AvailabilityMode string `gorm:"default:'a_demanda'" json:"availability_mode"`
	// PreparedDate: día (YYYY-MM-DD) del lote vigente de un plato por_porciones.
	// Si != hoy, el lote es viejo → el plato queda AGOTADO hasta re-preparar.
	PreparedDate *string `gorm:"type:varchar(10)" json:"prepared_date,omitempty"`

	// ── Spec F043 — Menú de restaurante ─────────────────────────────────
	// Description: texto del plato para el catálogo ("Hamburguesa artesanal
	// con queso, papas y bebida"). Vacío para productos retail normales.
	Description string `json:"description,omitempty"`
	// Portion: tamaño/porción libre ("Personal", "Para compartir", "300g").
	Portion string `json:"portion,omitempty"`
	// Spec 068 — Características del producto (texto libre multilínea, p. ej.
	// "Sin azúcar · Marca Nacional · Picante medio"). Aditivo, default vacío
	// (string, NUNCA uuid/date): el catálogo público lo muestra en el detalle.
	// Distinto de Description, que es el texto del PLATO de menú (F043).
	Characteristics string `json:"characteristics,omitempty"`
	// IsMenuItem marca un PLATO de menú de restaurante: alimenta la sección
	// "Menú restaurante" del catálogo público. Aditivo + default false: los
	// productos existentes no se ven afectados. Lo activan los 3 caminos del
	// módulo (cámara/manual/voz) y las recetas.
	IsMenuItem bool `gorm:"default:false;index" json:"is_menu_item"`

	// ── Spec 082 F3 — organización del catálogo online (aditivos) ─────────
	// HiddenInCatalog: el producto NO aparece en la tienda en línea (sigue en
	// el POS). IsFeatured: aparece DESTACADO (primero + estrella). Default
	// false → catálogo se ve como hoy.
	HiddenInCatalog bool `gorm:"default:false;index" json:"hidden_in_catalog"`
	IsFeatured      bool `gorm:"default:false;index" json:"is_featured"`

	// PhotoIsSample marca que ImageURL es una foto de MUESTRA generada por IA
	// a partir del nombre (una ilustración), NO la foto real del plato. El
	// catálogo público la etiqueta como "Imagen de muestra" para no engañar al
	// comensal (F043). Default false: una foto real subida (o mejorada
	// fielmente con IA) NO es muestra. Aditivo (Art. X).
	PhotoIsSample bool `gorm:"default:false" json:"photo_is_sample"`

	// ── Spec F044 — catálogo público unificado ──────────────────────────
	// IsService marca un SERVICIO publicable (corte de cabello, reparación,
	// mano de obra, domicilio…): se publica en el link público como "oferta"
	// junto a los platos, sin inventario y pedible siempre que la tienda esté
	// abierta. Espeja IsMenuItem (aditivo + default false). Generaliza el
	// catálogo a todo tipo de negocio, no solo restaurantes. Description se
	// reusa para el detalle del servicio.
	IsService bool `gorm:"default:false;index" json:"is_service"`

	// Spec 084 — comisión por SERVICIO (peluquería/barbería). Tasa por defecto
	// del servicio para el profesional que lo realiza; nullable = sin tasa propia
	// (cae a la del profesional o 0). Solo aplica cuando IsService=true.
	CommissionPct *float64 `gorm:"column:commission_pct;type:numeric(5,2)" json:"commission_pct,omitempty"`

	// Spec 084 Fase 2 — duración estimada del servicio en minutos, para armar la
	// agenda de citas (franjas disponibles). Nullable = sin duración definida.
	DurationMin *int `gorm:"column:duration_min" json:"duration_min,omitempty"`

	// ── Spec 063 — venta restringida a mayores de edad ──────────────────
	// IsAgeRestricted marca productos de venta SOLO para mayores de 18
	// (licor, cigarrillos, vapeadores…). El catálogo público exige
	// confirmar mayoría de edad antes de mostrarlos y los etiqueta "+18".
	IsAgeRestricted bool `gorm:"default:false" json:"is_age_restricted"`

	// ── Spec F029 — precios multi-tier por tipo de cliente ──────────────
	// PriceTier1 / PriceTier2 / PriceTier3 are optional per-tier prices
	// applied when Tenant.EnablePriceTiers is ON. Nullable (pointer)
	// distinguishes "not configured" from "configured to 0". Cuando el
	// tier elegido en POS no tiene precio para este producto, el carrito
	// hace fallback al `price` retail y muestra una nota visual.
	// Las columnas son ignoradas cuando la capacidad está OFF (AC-01).
	// Explicit `column:` tags pin the snake_case + underscore-before-digit
	// naming GORM otherwise omits (default would be `price_tier1`). The
	// frontend, F027 importer mapper, and the spec all use `price_tier_1`.
	PriceTier1 *float64 `gorm:"column:price_tier_1;type:numeric" json:"price_tier_1,omitempty"`
	PriceTier2 *float64 `gorm:"column:price_tier_2;type:numeric" json:"price_tier_2,omitempty"`
	PriceTier3 *float64 `gorm:"column:price_tier_3;type:numeric" json:"price_tier_3,omitempty"`
}
