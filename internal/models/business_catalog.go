// Spec: specs/041-catalogo-dinamico-modulos-tipos/spec.md
//
// Catálogo dinámico de módulos y tipos de negocio (F041). Mueve a la base
// de datos lo que hoy está hardcodeado en Flutter (dashboard_modules.dart /
// business_capability_map.dart) y en Go (tenant.go / business_capabilities.go),
// para que el super admin lo gestione sin releases y el dashboard del
// tendero lo refleje según su(s) tipo(s) de negocio.
//
// Aditivo y retrocompatible (Art. X): tablas nuevas; NO toca las columnas
// enable_* del Tenant — éstas conviven como "estado activado por tienda"
// (decisión D1 del spec).

package models

import "time"

// ── Enumeraciones (valores estables string, espejo del cliente) ──────────

// Tipo de render que la app usa para abrir un módulo.
const (
	RenderNative      = "native"      // mapea a una pantalla Flutter compilada
	RenderWebview     = "webview"     // abre una URL embebida
	RenderPlaceholder = "placeholder" // "próximamente / no disponible aún"
)

// Categorías fijas del dashboard (D8 — no administrables en esta fase).
const (
	CategoryVender     = "vender"
	CategoryInventario = "inventario"
	CategoryClientes   = "clientes"
	CategoryMiNegocio  = "mi_negocio"
)

// Nivel de relación módulo↔tipo (espejo de impliedCapabilities /
// defaultCapabilitiesForType).
const (
	RelationImplicit  = "implicit"  // el tipo lo concede, sin toggle
	RelationSuggested = "suggested" // pre-activado por defecto al elegir el tipo
	RelationAvailable = "available" // opt-in para cualquier tienda
)

// Estado forzado de un override por tienda.
const (
	OverrideActive   = "active"
	OverrideInactive = "inactive"
)

// ── Catálogo de módulos ──────────────────────────────────────────────────

// BusinessModule es una opción/módulo del dashboard, editable por el admin.
// La `Key` es la junta estable de interoperabilidad app↔backend↔admin y NO
// cambia tras crearse (editar metadatos nunca la altera — regla §7).
type BusinessModule struct {
	BaseModel

	Key         string `gorm:"uniqueIndex;not null" json:"key"`
	Name        string `gorm:"not null" json:"name"`
	Description string `gorm:"not null;default:''" json:"description"`
	IconKey     string `gorm:"not null;default:''" json:"icon_key"`
	Color       string `gorm:"not null;default:''" json:"color"`
	Category    string `gorm:"not null" json:"category"`

	RenderType string `gorm:"not null;default:'native'" json:"render_type"`
	// NativeScreenKey: clave del registro de pantallas en la app (si native).
	NativeScreenKey *string `json:"native_screen_key"`
	// WebviewURL: URL a embeber (si webview).
	WebviewURL *string `json:"webview_url"`
	// CapabilityKey: bandera enable_* del tenant que guarda el "activado por
	// tienda" para módulos opt-in (D1/D2). Nil para módulos core (siempre on).
	CapabilityKey *string `json:"capability_key"`

	RequiresPro bool `gorm:"not null;default:false" json:"requires_pro"`
	Active      bool `gorm:"not null;default:true" json:"active"`
	SortOrder   int  `gorm:"not null;default:0" json:"sort_order"`

	// ArchivedAt: archivar (no borrar) — la Key nunca se reutiliza (D6).
	ArchivedAt *time.Time `json:"archived_at"`

	CreatedBy string `gorm:"not null;default:''" json:"created_by"`
	UpdatedBy string `gorm:"not null;default:''" json:"updated_by"`
}

// ── Catálogo de tipos de negocio ─────────────────────────────────────────

// BusinessTypeCatalog es un tipo de negocio gestionable por el admin. El
// `Value` es estable y no se reutiliza (solo se archiva — D6).
type BusinessTypeCatalog struct {
	BaseModel

	Value     string `gorm:"uniqueIndex;not null" json:"value"`
	Label     string `gorm:"not null" json:"label"`
	IconKey   string `gorm:"not null;default:''" json:"icon_key"`
	Active    bool   `gorm:"not null;default:true" json:"active"`
	SortOrder int    `gorm:"not null;default:0" json:"sort_order"`

	ArchivedAt *time.Time `json:"archived_at"`

	CreatedBy string `gorm:"not null;default:''" json:"created_by"`
	UpdatedBy string `gorm:"not null;default:''" json:"updated_by"`
}

// ── Relación módulo ↔ tipo ───────────────────────────────────────────────

// ModuleTypeRelation liga un módulo a un tipo de negocio con un nivel.
// Único por (ModuleID, BusinessTypeValue).
type ModuleTypeRelation struct {
	BaseModel

	ModuleID          string `gorm:"not null;uniqueIndex:idx_mtr_module_type" json:"module_id"`
	BusinessTypeValue string `gorm:"not null;uniqueIndex:idx_mtr_module_type" json:"business_type_value"`
	RelationLevel     string `gorm:"not null" json:"relation_level"`
}

// ── Override por tienda ──────────────────────────────────────────────────

// TenantModuleOverride fuerza un módulo activo/inactivo en UNA tienda, sin
// alterar el catálogo global. Único por (TenantID, ModuleID).
type TenantModuleOverride struct {
	BaseModel

	TenantID    string `gorm:"not null;uniqueIndex:idx_tmo_tenant_module" json:"tenant_id"`
	ModuleID    string `gorm:"not null;uniqueIndex:idx_tmo_tenant_module" json:"module_id"`
	ForcedState string `gorm:"not null" json:"forced_state"`

	CreatedBy string `gorm:"not null;default:''" json:"created_by"`
}

// ── Log de auditoría (D9) ────────────────────────────────────────────────

// CatalogAuditLog registra cada escritura del catálogo con el antes/después.
// Before/After se guardan como JSON serializado (string) para portabilidad
// entre Postgres y el driver SQLite de los tests.
type CatalogAuditLog struct {
	BaseModel

	ActorID    string `gorm:"not null;default:''" json:"actor_id"`
	ActorName  string `gorm:"not null;default:''" json:"actor_name"`
	EntityType string `gorm:"index;not null" json:"entity_type"`
	EntityID   string `gorm:"index;not null;default:''" json:"entity_id"`
	Action     string `gorm:"not null" json:"action"`
	Before     string `gorm:"type:text" json:"before"`
	After      string `gorm:"type:text" json:"after"`
}
