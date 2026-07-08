// Spec: specs/100-completar-skus-inventario/spec.md
//
// Tests de los helpers compartidos de dedup de barcode (Spec 100 / D1):
// FindBarcodeOwner (lookup del producto dueño, tenant-scoped) y el
// clasificador IsProductBarcodeUniqueViolation (violación del índice único
// parcial en carrera).
package services_test

import (
	"errors"
	"testing"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupBarcodeOwnerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Product{}))
	return db
}

func TestFindBarcodeOwner(t *testing.T) {
	db := setupBarcodeOwnerDB(t)
	owner := models.Product{
		BaseModel: models.BaseModel{ID: "aaaaaaaa-0000-4000-8000-000000000001"},
		TenantID:  "tenant-a", Name: "Coca-Cola", Barcode: "7701234567890", Price: 3000,
	}
	require.NoError(t, db.Create(&owner).Error)

	t.Run("dueño en el mismo tenant", func(t *testing.T) {
		got := services.FindBarcodeOwner(db, "tenant-a", "7701234567890", "otro-id")
		require.NotNil(t, got)
		assert.Equal(t, owner.ID, got.ID)
	})
	t.Run("el propio producto no conflictúa", func(t *testing.T) {
		assert.Nil(t, services.FindBarcodeOwner(db, "tenant-a", "7701234567890", owner.ID))
	})
	t.Run("otro tenant no conflictúa", func(t *testing.T) {
		assert.Nil(t, services.FindBarcodeOwner(db, "tenant-b", "7701234567890", "otro-id"))
	})
	t.Run("barcode vacío no chequea", func(t *testing.T) {
		assert.Nil(t, services.FindBarcodeOwner(db, "tenant-a", "", "otro-id"))
	})
	t.Run("soft-deleted no bloquea", func(t *testing.T) {
		require.NoError(t, db.Delete(&owner).Error)
		assert.Nil(t, services.FindBarcodeOwner(db, "tenant-a", "7701234567890", "otro-id"))
	})
}

func TestIsProductBarcodeUniqueViolation(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"violación por nombre de índice",
			errors.New(`ERROR: duplicate key value violates unique constraint "idx_products_tenant_barcode_unique" (SQLSTATE 23505)`),
			true},
		{"otro índice único no aplica",
			errors.New(`ERROR: duplicate key value violates unique constraint "uq_customer_phone" (SQLSTATE 23505)`),
			false},
		{"error cualquiera", errors.New("connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, services.IsProductBarcodeUniqueViolation(tc.err))
		})
	}
}
