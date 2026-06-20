// Spec: specs/070-galeria-multimedia-producto/spec.md
package models

// ProductMedia — media EXTRA de un producto/ítem (Spec 070): imágenes
// adicionales, links de YouTube y videos cortos subidos. ADITIVO: la foto
// principal sigue viviendo en Product.PhotoURL/ImageURL (no se migra) y es
// siempre el item posición 0 del carrusel, inyectado al vuelo en el builder.
// Un Product sin filas ProductMedia se comporta EXACTAMENTE como hoy.
//
// Reglas (lecciones Spec 066 / feedback_nullable_uuid_rule): ninguna columna
// uuid con DEFAULT vacío; los nullable van como punteros.
type ProductMedia struct {
	BaseModel

	TenantID  string `gorm:"type:uuid;not null;index" json:"tenant_id"`
	ProductID string `gorm:"type:uuid;not null;index" json:"product_id"`
	// Type: "image" | "youtube" | "video".
	Type string `gorm:"type:varchar(16);not null;index" json:"type"`
	// URL pública en R2 (image/video) o link canónico de YouTube.
	URL string `gorm:"not null" json:"url"`
	// Thumbnail: poster del video o miniatura de YouTube; null para imagen.
	Thumbnail *string `gorm:"type:varchar(512)" json:"thumbnail,omitempty"`
	// Position: orden en el carrusel (solo media extra; la principal es 0).
	Position int `gorm:"default:0;index" json:"position"`
	// YouTubeID: 11 chars; null salvo type youtube.
	YouTubeID *string `gorm:"type:varchar(16)" json:"youtube_id,omitempty"`
	// DurationS: segundos (<=25); null para image/youtube.
	DurationS *int `gorm:"column:duration_s" json:"duration_s,omitempty"`
	// StorageKey: key del objeto en R2 (para poder borrarlo); null para youtube.
	StorageKey *string `gorm:"type:varchar(512)" json:"storage_key,omitempty"`
	SizeBytes  *int64  `json:"size_bytes,omitempty"`
}

// Tipos de media admitidos.
const (
	MediaTypeImage   = "image"
	MediaTypeYouTube = "youtube"
	MediaTypeVideo   = "video"
)

// MaxMediaPerProduct — tope de media EXTRA por producto (sin contar la foto
// principal). Evita que el almacenamiento se llene.
const MaxMediaPerProduct = 6

// MaxVideoDurationS — límite duro del video corto subido.
const MaxVideoDurationS = 25
