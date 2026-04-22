package database

import (
	"testing"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupBootstrapDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// Hand-crafted schema — admin_users's production DDL uses
	// gen_random_uuid() which SQLite can't parse. The only columns
	// the bootstrap touches are email / password_hash / name /
	// is_super_admin.
	require.NoError(t, db.Exec(`
		CREATE TABLE admin_users (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME,
			email TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			name TEXT NOT NULL,
			is_super_admin INTEGER DEFAULT 1
		);
		CREATE UNIQUE INDEX idx_admin_users_email ON admin_users(email);
	`).Error)
	return db
}

func TestBootstrapSuperAdmin_NoOpWhenEnvUnset(t *testing.T) {
	db := setupBootstrapDB(t)

	require.NoError(t, BootstrapSuperAdmin(db, BootstrapSuperAdminConfig{}))

	var count int64
	require.NoError(t, db.Model(&models.AdminUser{}).Count(&count).Error)
	assert.Zero(t, count, "missing email/password must not seed a row")
}

func TestBootstrapSuperAdmin_InsertsWhenBothSet(t *testing.T) {
	db := setupBootstrapDB(t)

	require.NoError(t, BootstrapSuperAdmin(db, BootstrapSuperAdminConfig{
		Email:    "bryan@vendia.co",
		Password: "super-secret-42",
		Name:     "Bryan",
	}))

	var admin models.AdminUser
	require.NoError(t, db.Where("email = ?", "bryan@vendia.co").First(&admin).Error)
	assert.Equal(t, "Bryan", admin.Name)
	assert.True(t, admin.IsSuperAdmin)
	// Hash is valid bcrypt — it matches the original password.
	assert.NoError(t,
		bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash),
			[]byte("super-secret-42")))
}

func TestBootstrapSuperAdmin_DefaultsNameToEmailLocalPart(t *testing.T) {
	db := setupBootstrapDB(t)

	require.NoError(t, BootstrapSuperAdmin(db, BootstrapSuperAdminConfig{
		Email:    "ops@vendia.co",
		Password: "x",
	}))

	var admin models.AdminUser
	require.NoError(t, db.Where("email = ?", "ops@vendia.co").First(&admin).Error)
	assert.Equal(t, "ops", admin.Name,
		"when SEED_ADMIN_NAME is empty, fall back to the email local part")
}

func TestBootstrapSuperAdmin_NormalisesEmail(t *testing.T) {
	db := setupBootstrapDB(t)

	require.NoError(t, BootstrapSuperAdmin(db, BootstrapSuperAdminConfig{
		Email:    "  BRYAN@VENDIA.CO  ",
		Password: "x",
	}))

	var admin models.AdminUser
	require.NoError(t, db.Where("email = ?", "bryan@vendia.co").First(&admin).Error)
	assert.Equal(t, "bryan@vendia.co", admin.Email)
}

func TestBootstrapSuperAdmin_IdempotentOnSecondBoot(t *testing.T) {
	db := setupBootstrapDB(t)

	// First boot with an initial password.
	require.NoError(t, BootstrapSuperAdmin(db, BootstrapSuperAdminConfig{
		Email: "bryan@vendia.co", Password: "first-password", Name: "Bryan",
	}))
	var first models.AdminUser
	require.NoError(t, db.Where("email = ?", "bryan@vendia.co").First(&first).Error)

	// Second boot with a DIFFERENT password — the existing row must
	// not be overwritten (rotation happens through an authenticated
	// session, not env-var restarts).
	require.NoError(t, BootstrapSuperAdmin(db, BootstrapSuperAdminConfig{
		Email: "bryan@vendia.co", Password: "different-password", Name: "Someone Else",
	}))

	var second models.AdminUser
	require.NoError(t, db.Where("email = ?", "bryan@vendia.co").First(&second).Error)
	assert.Equal(t, first.PasswordHash, second.PasswordHash,
		"second boot must NOT rotate the password")
	assert.Equal(t, "Bryan", second.Name, "name stays put too")

	// The original password still validates; the "different" one
	// does NOT — we didn't rotate.
	assert.NoError(t,
		bcrypt.CompareHashAndPassword([]byte(second.PasswordHash),
			[]byte("first-password")))
	assert.Error(t,
		bcrypt.CompareHashAndPassword([]byte(second.PasswordHash),
			[]byte("different-password")))
}

func TestBootstrapSuperAdmin_RejectsEmailOrPasswordAlone(t *testing.T) {
	db := setupBootstrapDB(t)

	// Missing password
	require.NoError(t, BootstrapSuperAdmin(db,
		BootstrapSuperAdminConfig{Email: "bryan@vendia.co"}))
	var count int64
	db.Model(&models.AdminUser{}).Count(&count)
	assert.Zero(t, count)

	// Missing email
	require.NoError(t, BootstrapSuperAdmin(db,
		BootstrapSuperAdminConfig{Password: "x"}))
	db.Model(&models.AdminUser{}).Count(&count)
	assert.Zero(t, count, "email alone without password must also no-op")
}
