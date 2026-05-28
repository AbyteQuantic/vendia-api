// Spec: specs/038-push-notifications-web-android/spec.md
package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupNotificationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// Notification usa `gen_random_uuid()` como default — Postgres-only.
	// El BeforeCreate del modelo cubre el UUID en SQLite, así que para
	// que AutoMigrate no falle creamos la tabla a mano (mismo patrón
	// que internal/handlers/quotes_test.go). Incluimos los 3 campos
	// nuevos de Spec 038 con sus tipos retrocompatibles.
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS notifications (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			tenant_id TEXT NOT NULL,
			title TEXT NOT NULL,
			body TEXT DEFAULT '',
			type TEXT DEFAULT 'info',
			is_read INTEGER DEFAULT 0,
			deep_link TEXT,
			pushed_at DATETIME,
			dedup_key TEXT
		)
	`).Error)
	return db
}

// T-04a — Un Notification antiguo (sin los 3 campos nuevos) se crea y
// se recupera sin error, con los nuevos campos en NULL. Es el test
// del Artículo X: la migración debe ser retrocompatible — clientes
// que crean filas sin estos campos NO deben romperse.
func TestNotification_BackwardCompatibleWithoutNewFields(t *testing.T) {
	db := setupNotificationDB(t)

	n := Notification{
		TenantID: "11111111-1111-1111-1111-111111111111",
		Title:    "Pedido viejo (sin deep_link)",
		Body:     "Cliente Pedro pidió 1 unidad",
		Type:     "info",
	}
	require.NoError(t, db.Create(&n).Error)

	var reloaded Notification
	require.NoError(t, db.First(&reloaded, "id = ?", n.ID).Error)
	assert.Equal(t, "Pedido viejo (sin deep_link)", reloaded.Title)
	assert.Nil(t, reloaded.DeepLink, "DeepLink debe quedar NULL si no se setea")
	assert.Nil(t, reloaded.PushedAt, "PushedAt debe quedar NULL si no se ha enviado push")
	assert.Nil(t, reloaded.DedupKey, "DedupKey debe quedar NULL en el caso por defecto")
}

// T-04b — Un Notification nuevo (con los 3 campos) los persiste y
// recupera correctamente; PushedAt es un puntero a time.Time para
// distinguir "no enviado" (NULL) de "enviado en t=zero" (caso
// teórico).
func TestNotification_PersistsNewFields(t *testing.T) {
	db := setupNotificationDB(t)

	deepLink := "/pedidos/abc-123"
	pushedAt := time.Now().UTC()
	dedup := "web-order:abc-123"

	n := Notification{
		TenantID: "11111111-1111-1111-1111-111111111111",
		Title:    "Pedido nuevo",
		Body:     "Pedro pidió 2 unidades",
		Type:     "web_order",
		DeepLink: &deepLink,
		PushedAt: &pushedAt,
		DedupKey: &dedup,
	}
	require.NoError(t, db.Create(&n).Error)

	var reloaded Notification
	require.NoError(t, db.First(&reloaded, "id = ?", n.ID).Error)
	require.NotNil(t, reloaded.DeepLink)
	assert.Equal(t, deepLink, *reloaded.DeepLink)
	require.NotNil(t, reloaded.PushedAt)
	assert.WithinDuration(t, pushedAt, *reloaded.PushedAt, time.Second)
	require.NotNil(t, reloaded.DedupKey)
	assert.Equal(t, dedup, *reloaded.DedupKey)
}
