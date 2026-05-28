// Spec: specs/038-push-notifications-web-android/spec.md
package push

import (
	"context"
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupDispatcherDB monta una BD SQLite con las tablas que el
// dispatcher consulta. La tabla `notifications` se crea con SQL
// crudo porque su default `gen_random_uuid()` es Postgres-only
// (mismo patrón que internal/handlers/quotes_test.go).
func setupDispatcherDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// Modelos que sí migran limpio.
	require.NoError(t, db.AutoMigrate(
		&models.DeviceToken{},
		&models.Tenant{},
		&models.TenantSubscription{},
		&models.Product{},
	))

	// Notifications a mano (gen_random_uuid no soportado en SQLite).
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

// seedActiveTenant crea un tenant activo (Subscription TRIAL no
// expirado) y un usuario propietario. Retorna sus IDs. Es la
// configuración por defecto de un POS funcional.
// seedActiveTenant rellena `phone` con un valor único derivado del
// `tenantID` para evitar el UNIQUE constraint cuando un test seede
// múltiples tenants (caso aislamiento cross-tenant).
func seedActiveTenant(t *testing.T, db *gorm.DB, tenantID string) string {
	t.Helper()
	require.NoError(t, db.Create(&models.Tenant{
		BaseModel:    models.BaseModel{ID: tenantID},
		BusinessName: "Tienda Test",
		Phone:        "phone-" + tenantID[:8],
	}).Error)
	future := time.Now().Add(7 * 24 * time.Hour)
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID:    tenantID,
		Status:      models.SubscriptionStatusTrial,
		TrialEndsAt: &future,
	}).Error)
	return tenantID
}

func seedSuspendedTenant(t *testing.T, db *gorm.DB, tenantID string) string {
	t.Helper()
	require.NoError(t, db.Create(&models.Tenant{
		BaseModel:    models.BaseModel{ID: tenantID},
		BusinessName: "Tienda Morosa",
		Phone:        "phone-" + tenantID[:8],
	}).Error)
	require.NoError(t, db.Create(&models.TenantSubscription{
		TenantID: tenantID,
		Status:   models.SubscriptionStatusProPastDue,
	}).Error)
	return tenantID
}

func seedToken(t *testing.T, db *gorm.DB, tenantID, userID, token string) {
	t.Helper()
	require.NoError(t, db.Create(&models.DeviceToken{
		TenantID:   tenantID,
		UserID:     userID,
		Token:      token,
		Platform:   models.DeviceTokenPlatformWeb,
		LastSeenAt: time.Now(),
	}).Error)
}

func newDispatcherWith(fake *FakeSender, now time.Time) *Dispatcher {
	d := NewDispatcher(fake)
	d.nowFn = func() time.Time { return now }
	return d
}

// ─── T-06a-1 — Happy path ────────────────────────────────────────────
//
// Un tenant activo con 2 tokens activos recibe un evento; el dispatcher
// debe: (1) crear UNA Notification in-app, (2) llamar al sender con
// AMBOS tokens, (3) marcar `pushed_at` de la notification, (4) no
// invalidar nada. Cubre AC-04 (pedido web → push), AC-06 (promo),
// AC-07 (fiado), AC-17 (admin + cashier reciben igual).
func TestDispatcher_HappyPath(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantID := "11111111-1111-1111-1111-111111111111"
	userAdmin := "22222222-2222-2222-2222-222222222222"
	userCashier := "33333333-3333-3333-3333-333333333333"
	seedActiveTenant(t, db, tenantID)
	seedToken(t, db, tenantID, userAdmin, "tok-admin")
	seedToken(t, db, tenantID, userCashier, "tok-cashier")

	fake := &FakeSender{}
	d := newDispatcherWith(fake, time.Now())

	out, err := d.DispatchEvent(context.Background(), db, Event{
		TenantID: tenantID,
		Type:     "web_order",
		Title:    "Pedido nuevo",
		Body:     "Pedro pidió 2 unidades",
		DeepLink: "/pedidos/abc",
		DedupKey: "web-order:abc",
	})
	require.NoError(t, err)
	assert.Equal(t, OutcomeSent, out.Status)
	assert.Equal(t, 2, out.TokensSent)
	assert.Equal(t, 0, out.TokensInvalid)
	assert.NotEmpty(t, out.NotificationID)

	// In-app row exists with the correct fields.
	var n models.Notification
	require.NoError(t, db.First(&n, "id = ?", out.NotificationID).Error)
	assert.Equal(t, "Pedido nuevo", n.Title)
	require.NotNil(t, n.PushedAt)
	require.NotNil(t, n.DeepLink)
	assert.Equal(t, "/pedidos/abc", *n.DeepLink)
	require.NotNil(t, n.DedupKey)
	assert.Equal(t, "web-order:abc", *n.DedupKey)

	// Sender was called once with both tokens.
	require.Len(t, fake.Calls, 1)
	assert.ElementsMatch(t, []string{"tok-admin", "tok-cashier"}, fake.Calls[0].Tokens)
	assert.Equal(t, "Pedido nuevo", fake.Calls[0].Payload.Title)
	assert.Equal(t, "/pedidos/abc", fake.Calls[0].Payload.DeepLink)
}

// ─── T-06a-2 — Tenant suspendido: skip TODO (FR-17, AC-16) ───────────
func TestDispatcher_SuspendedTenantSkipsEverything(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantID := "44444444-4444-4444-4444-444444444444"
	seedSuspendedTenant(t, db, tenantID)
	seedToken(t, db, tenantID, "user", "tok-x")

	fake := &FakeSender{}
	d := newDispatcherWith(fake, time.Now())

	out, err := d.DispatchEvent(context.Background(), db, Event{
		TenantID: tenantID,
		Type:     "web_order",
		Title:    "Pedido",
		Body:     "body",
	})
	require.NoError(t, err)
	assert.Equal(t, OutcomeSkippedSuspended, out.Status)
	assert.Empty(t, out.NotificationID)
	assert.Empty(t, fake.Calls, "tenant suspendido NO debe disparar Send")

	// Tampoco debe haber Notification row creado.
	var count int64
	db.Model(&models.Notification{}).Where("tenant_id = ?", tenantID).Count(&count)
	assert.EqualValues(t, 0, count, "tenant suspendido tampoco genera in-app desde el dispatcher")
}

// ─── T-06a-3 — Cap diario alcanzado: in-app sí, push no (FR-16, AC-15)
//
// Tenant ya tiene 20 notifications con `pushed_at` hoy. El evento 21 se
// registra in-app pero el sender NO se llama.
func TestDispatcher_DailyCapReached_InAppOnly(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantID := "55555555-5555-5555-5555-555555555555"
	seedActiveTenant(t, db, tenantID)
	seedToken(t, db, tenantID, "user", "tok")

	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	// Sembrar 20 notificaciones todas dentro del mismo día (espaciadas
	// por minutos para no caer al día anterior).
	for i := 0; i < 20; i++ {
		pushedAt := now.Add(-time.Duration(i) * time.Minute)
		require.NoError(t, db.Exec(`INSERT INTO notifications (id, created_at, tenant_id, title, type, pushed_at) VALUES (?, ?, ?, ?, ?, ?)`,
			"seed-"+timeKey(i), now, tenantID, "viejo", "info", pushedAt,
		).Error)
	}

	fake := &FakeSender{}
	d := newDispatcherWith(fake, now)

	out, err := d.DispatchEvent(context.Background(), db, Event{
		TenantID: tenantID, Type: "promo", Title: "Promo 21", Body: "body",
	})
	require.NoError(t, err)
	assert.Equal(t, OutcomeCapReached, out.Status)
	assert.NotEmpty(t, out.NotificationID, "in-app SÍ se crea (FR-16)")
	assert.Empty(t, fake.Calls, "push NO se envía cuando se alcanzó el cap")

	// La nueva notification no tiene pushed_at (no se envió).
	var n models.Notification
	require.NoError(t, db.First(&n, "id = ?", out.NotificationID).Error)
	assert.Nil(t, n.PushedAt)
}

// ─── T-06a-4 — Dedup dentro de 5 min: skip TODO (FR-13, AC-13) ───────
func TestDispatcher_DedupWithinWindowSkips(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantID := "66666666-6666-6666-6666-666666666666"
	seedActiveTenant(t, db, tenantID)
	seedToken(t, db, tenantID, "user", "tok")

	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	twoMinAgo := now.Add(-2 * time.Minute)
	dedup := "web-order:order-123"

	// Sembrar una notification reciente con la misma dedup_key.
	require.NoError(t, db.Exec(`INSERT INTO notifications (id, created_at, tenant_id, title, type, pushed_at, dedup_key) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"prev", twoMinAgo, tenantID, "viejo", "web_order", twoMinAgo, dedup,
	).Error)

	fake := &FakeSender{}
	d := newDispatcherWith(fake, now)

	out, err := d.DispatchEvent(context.Background(), db, Event{
		TenantID: tenantID, Type: "web_order", Title: "Dup", Body: "body", DedupKey: dedup,
	})
	require.NoError(t, err)
	assert.Equal(t, OutcomeSkippedDuplicate, out.Status)
	assert.Empty(t, out.NotificationID)
	assert.Empty(t, fake.Calls, "evento duplicado NO debe disparar Send")
}

// ─── T-06a-5 — Dedup fuera de 5 min: SÍ envía ────────────────────────
func TestDispatcher_DedupOutsideWindowSends(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantID := "77777777-7777-7777-7777-777777777777"
	seedActiveTenant(t, db, tenantID)
	seedToken(t, db, tenantID, "user", "tok")

	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	sixMinAgo := now.Add(-6 * time.Minute)
	dedup := "promo:abc"

	require.NoError(t, db.Exec(`INSERT INTO notifications (id, created_at, tenant_id, title, type, pushed_at, dedup_key) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"prev", sixMinAgo, tenantID, "viejo", "promo", sixMinAgo, dedup,
	).Error)

	fake := &FakeSender{}
	d := newDispatcherWith(fake, now)

	out, err := d.DispatchEvent(context.Background(), db, Event{
		TenantID: tenantID, Type: "promo", Title: "Reusable", Body: "body", DedupKey: dedup,
	})
	require.NoError(t, err)
	assert.Equal(t, OutcomeSent, out.Status)
	assert.Len(t, fake.Calls, 1)
}

// ─── T-06a-6 — Token reportado inválido por sender se invalida (AC-10)
func TestDispatcher_InvalidTokenGetsMarked(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantID := "88888888-8888-8888-8888-888888888888"
	seedActiveTenant(t, db, tenantID)
	seedToken(t, db, tenantID, "u-good", "tok-good")
	seedToken(t, db, tenantID, "u-bad", "tok-bad")

	fake := &FakeSender{InvalidateTokens: map[string]bool{"tok-bad": true}}
	d := newDispatcherWith(fake, time.Now())

	out, err := d.DispatchEvent(context.Background(), db, Event{
		TenantID: tenantID, Type: "test", Title: "x", Body: "y",
	})
	require.NoError(t, err)
	assert.Equal(t, OutcomeSent, out.Status)
	assert.Equal(t, 1, out.TokensSent)
	assert.Equal(t, 1, out.TokensInvalid)

	// tok-bad row debe tener invalidated_at; tok-good no.
	var bad, good models.DeviceToken
	require.NoError(t, db.First(&bad, "token = ?", "tok-bad").Error)
	require.NoError(t, db.First(&good, "token = ?", "tok-good").Error)
	assert.NotNil(t, bad.InvalidatedAt, "tok-bad debe quedar invalidado")
	assert.Nil(t, good.InvalidatedAt, "tok-good sigue activo")
}

// ─── T-06a-7 — Tokens ya invalidados NO se incluyen en el envío ──────
func TestDispatcher_ExcludesInvalidatedTokens(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantID := "99999999-9999-9999-9999-999999999999"
	seedActiveTenant(t, db, tenantID)
	seedToken(t, db, tenantID, "u-active", "tok-active")
	// Token ya invalidado:
	past := time.Now().Add(-time.Hour)
	require.NoError(t, db.Create(&models.DeviceToken{
		TenantID: tenantID, UserID: "u-dead",
		Token: "tok-dead", Platform: models.DeviceTokenPlatformWeb,
		LastSeenAt: time.Now(), InvalidatedAt: &past,
	}).Error)

	fake := &FakeSender{}
	d := newDispatcherWith(fake, time.Now())

	out, err := d.DispatchEvent(context.Background(), db, Event{
		TenantID: tenantID, Type: "test", Title: "x", Body: "y",
	})
	require.NoError(t, err)
	assert.Equal(t, OutcomeSent, out.Status)
	require.Len(t, fake.Calls, 1)
	assert.ElementsMatch(t, []string{"tok-active"}, fake.Calls[0].Tokens,
		"tok-dead NO debe estar en el envío")
}

// ─── T-06a-8 — Aislamiento cross-tenant (AC-11) — CRÍTICO Art. III ───
func TestDispatcher_CrossTenantIsolation(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantA := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	tenantB := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	seedActiveTenant(t, db, tenantA)
	seedActiveTenant(t, db, tenantB)
	seedToken(t, db, tenantA, "u-a", "tok-A")
	seedToken(t, db, tenantB, "u-b", "tok-B")

	fake := &FakeSender{}
	d := newDispatcherWith(fake, time.Now())

	// Evento en tenant A.
	_, err := d.DispatchEvent(context.Background(), db, Event{
		TenantID: tenantA, Type: "test", Title: "evento de A",
	})
	require.NoError(t, err)

	require.Len(t, fake.Calls, 1)
	assert.ElementsMatch(t, []string{"tok-A"}, fake.Calls[0].Tokens,
		"tok-B del tenant B NUNCA debe recibir el push de un evento de tenant A")

	// La notification in-app debe pertenecer al tenant A.
	var notifs []models.Notification
	require.NoError(t, db.Find(&notifs).Error)
	require.Len(t, notifs, 1)
	assert.Equal(t, tenantA, notifs[0].TenantID)
}

// ─── T-06a-9 — Tenant sin tokens: in-app SÍ, send no ─────────────────
func TestDispatcher_NoTokensStillCreatesInApp(t *testing.T) {
	db := setupDispatcherDB(t)
	tenantID := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	seedActiveTenant(t, db, tenantID)
	// Sin tokens.

	fake := &FakeSender{}
	d := newDispatcherWith(fake, time.Now())

	out, err := d.DispatchEvent(context.Background(), db, Event{
		TenantID: tenantID, Type: "test", Title: "evento", Body: "body",
	})
	require.NoError(t, err)
	assert.Equal(t, OutcomeNoTokens, out.Status)
	assert.NotEmpty(t, out.NotificationID, "in-app row debe crearse aunque no haya tokens (Art. II)")
	assert.Empty(t, fake.Calls)

	// La notification queda sin pushed_at (no se llegó a enviar).
	var n models.Notification
	require.NoError(t, db.First(&n, "id = ?", out.NotificationID).Error)
	assert.Nil(t, n.PushedAt)
}

// helper para generar IDs únicos en el seed del cap.
func timeKey(i int) string {
	return time.Now().Add(time.Duration(i) * time.Millisecond).Format("150405.000000000")
}
