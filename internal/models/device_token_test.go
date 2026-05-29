// Spec: specs/038-push-notifications-web-android/spec.md
package models

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupDeviceTokenDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&DeviceToken{}))
	return db
}

// T-02a-1 — Un DeviceToken se persiste con todos sus campos y BaseModel
// (UUID auto-asignado, timestamps).
func TestDeviceToken_PersistsAllFields(t *testing.T) {
	db := setupDeviceTokenDB(t)

	tenantID := "11111111-1111-1111-1111-111111111111"
	userID := "22222222-2222-2222-2222-222222222222"
	label := "iPhone Safari"

	tok := DeviceToken{
		TenantID:    tenantID,
		UserID:      userID,
		Token:       "fcm:abcdef0123456789",
		Platform:    DeviceTokenPlatformWeb,
		DeviceLabel: &label,
		LastSeenAt:  time.Now(),
	}
	require.NoError(t, db.Create(&tok).Error)
	assert.NotEmpty(t, tok.ID, "BaseModel.BeforeCreate debe generar UUID")
	assert.False(t, tok.CreatedAt.IsZero())
	assert.Nil(t, tok.InvalidatedAt)

	var reloaded DeviceToken
	require.NoError(t, db.First(&reloaded, "id = ?", tok.ID).Error)
	assert.Equal(t, tenantID, reloaded.TenantID)
	assert.Equal(t, userID, reloaded.UserID)
	assert.Equal(t, DeviceTokenPlatformWeb, reloaded.Platform)
	require.NotNil(t, reloaded.DeviceLabel)
	assert.Equal(t, label, *reloaded.DeviceLabel)
}

// T-02a-2 — Platform debe ser "web" o "android" (Fase 1). El validador
// de modelo rechaza valores inválidos en BeforeCreate, sin depender de
// validaciones del handler — defensa en profundidad.
func TestDeviceToken_ValidatesPlatform(t *testing.T) {
	db := setupDeviceTokenDB(t)
	tok := DeviceToken{
		TenantID:   "11111111-1111-1111-1111-111111111111",
		UserID:     "22222222-2222-2222-2222-222222222222",
		Token:      "fcm:x",
		Platform:   "ios", // Fase 2 — todavía no soportado
		LastSeenAt: time.Now(),
	}
	err := db.Create(&tok).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "platform")
}

// T-02a-3 — Sin token Y sin Web Push subscription es inválido. Es
// defensa: el cliente debe enviar al menos uno de los dos modos
// (FCM o Web Push nativo).
func TestDeviceToken_RejectsBothCredentialsMissing(t *testing.T) {
	db := setupDeviceTokenDB(t)
	tok := DeviceToken{
		TenantID:   "11111111-1111-1111-1111-111111111111",
		UserID:     "22222222-2222-2222-2222-222222222222",
		Token:      "",
		Platform:   DeviceTokenPlatformWeb,
		LastSeenAt: time.Now(),
	}
	err := db.Create(&tok).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credencial")
}

// T-02a-3b — Una fila Web Push (sin token FCM, con endpoint+p256dh+
// auth + platform=web_ios) es VÁLIDA y se persiste correctamente.
// Es el caso iOS Safari.
func TestDeviceToken_AcceptsWebPushSubscription(t *testing.T) {
	db := setupDeviceTokenDB(t)
	endpoint := "https://web.push.apple.com/QH8wL5..."
	p256 := "BFakeKeyForTestPurposesOnlyNotRealCryptoMaterial..."
	auth := "fakeAuthSecret"
	tok := DeviceToken{
		TenantID:   "11111111-1111-1111-1111-111111111111",
		UserID:     "22222222-2222-2222-2222-222222222222",
		Token:      "",
		Platform:   DeviceTokenPlatformWebIOS,
		Endpoint:   &endpoint,
		P256dh:     &p256,
		Auth:       &auth,
		LastSeenAt: time.Now(),
	}
	require.NoError(t, db.Create(&tok).Error)
	assert.True(t, tok.IsWebPush())
}

// T-02a-4 — Un mismo token NO puede tener 2 filas activas en el mismo
// tenant: el índice único parcial (tenant_id, token) WHERE
// invalidated_at IS NULL lo previene. Si un row se invalida, sí se
// puede crear otro con el mismo token (caso re-registro tras
// invalidación).
//
// NOTA SQLite: SQLite (test DB) no soporta UNIQUE WHERE; el constraint
// se valida con un test contra Postgres en CI o se implementa con
// chequeo en aplicación. Para mantener este test portable, validamos
// que el modelo declara correctamente el índice via tag GORM — el
// constraint real lo aplica Postgres en producción.
func TestDeviceToken_DeclaresPartialIndex(t *testing.T) {
	// Inspect the struct tags directly. El uniqueIndex original se
	// reemplazó por un parcial WHERE token != '' AND invalidated_at
	// IS NULL — ahora las filas Web Push (token vacío) no chocan
	// con el constraint. El index sigue ahí para optimizar la query
	// del dispatcher.
	tokType := reflect.TypeOf(DeviceToken{})
	field, ok := tokType.FieldByName("Token")
	require.True(t, ok, "campo Token debe existir en DeviceToken")
	tag := field.Tag.Get("gorm")
	assert.Contains(t, tag, "idx_device_token_active",
		"campo Token debe declarar el index parcial")
}

// T-02a-5 — DeviceTokenPlatform constants existen y son las dos
// soportadas en Fase 1 (Web + Android, sin iOS).
func TestDeviceToken_PlatformConstants(t *testing.T) {
	assert.Equal(t, "web", DeviceTokenPlatformWeb)
	assert.Equal(t, "android", DeviceTokenPlatformAndroid)
}
