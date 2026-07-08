// Spec: specs/100-completar-skus-inventario/spec.md
//
// Spec 100 / D1 — dedup de barcode en el importador CSV. La rama "update por
// nombre" de processProductImportRow escribía updates["barcode"] sin pasar
// por FindBarcodeOwner: si el código pertenecía a OTRO producto vivo del
// tenant, el error crudo de Postgres (inglés + constraint + SQLSTATE 23505)
// llegaba tal cual al tendero en importFailedRow. Contrato esperado: razón
// en ESPAÑOL con el nombre del producto dueño, sin internals de la BD; y la
// carrera (violación del índice único) mapeada a la misma razón limpia.
package handlers

import (
	"errors"
	"testing"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImportBarcodeConflict_SpanishReasonWithOwnerName(t *testing.T) {
	db := setupProductImportDB(t)
	owner := models.Product{
		BaseModel: models.BaseModel{ID: "aaaaaaaa-0000-4000-8000-000000000001"},
		TenantID:  "tenant-csv", Name: "Coca-Cola", Barcode: "7701234567890", Price: 3000,
	}
	require.NoError(t, db.Create(&owner).Error)

	failed := importBarcodeConflict(db, "tenant-csv", 3, "7701234567890",
		"bbbbbbbb-0000-4000-8000-000000000002")
	require.NotNil(t, failed, "barcode de otro producto vivo debe fallar la fila")
	assert.Equal(t, 3, failed.RowIndex)
	assert.Contains(t, failed.Reason, "Coca-Cola", "la razón debe nombrar al producto dueño")
	assert.NotContains(t, failed.Reason, "SQLSTATE", "sin internals de la BD")
	assert.NotContains(t, failed.Reason, "constraint", "sin internals de la BD")
}

func TestImportBarcodeConflict_NoConflictCases(t *testing.T) {
	db := setupProductImportDB(t)
	owner := models.Product{
		BaseModel: models.BaseModel{ID: "aaaaaaaa-0000-4000-8000-000000000001"},
		TenantID:  "tenant-csv", Name: "Coca-Cola", Barcode: "7701234567890", Price: 3000,
	}
	require.NoError(t, db.Create(&owner).Error)

	t.Run("el propio producto (update por nombre del dueño) no conflictúa", func(t *testing.T) {
		assert.Nil(t, importBarcodeConflict(db, "tenant-csv", 0, "7701234567890", owner.ID))
	})
	t.Run("otro tenant no conflictúa", func(t *testing.T) {
		assert.Nil(t, importBarcodeConflict(db, "tenant-otro", 0, "7701234567890", "x"))
	})
	t.Run("barcode vacío no chequea", func(t *testing.T) {
		assert.Nil(t, importBarcodeConflict(db, "tenant-csv", 0, "", "x"))
	})
}

func TestImportBarcodeFailedRow_MapsRaceViolationToSpanish(t *testing.T) {
	db := setupProductImportDB(t)
	owner := models.Product{
		BaseModel: models.BaseModel{ID: "aaaaaaaa-0000-4000-8000-000000000001"},
		TenantID:  "tenant-csv", Name: "Coca-Cola", Barcode: "7701234567890", Price: 3000,
	}
	require.NoError(t, db.Create(&owner).Error)

	raceErr := errors.New(`ERROR: duplicate key value violates unique constraint "idx_products_tenant_barcode_unique" (SQLSTATE 23505)`)

	t.Run("violación del índice → razón limpia con el dueño", func(t *testing.T) {
		failed := importBarcodeFailedRow(db, "tenant-csv", 7, "7701234567890", "otro-id", raceErr)
		require.NotNil(t, failed)
		assert.Equal(t, 7, failed.RowIndex)
		assert.Contains(t, failed.Reason, "Coca-Cola")
		assert.NotContains(t, failed.Reason, "SQLSTATE")
	})
	t.Run("dueño ilegible tras la carrera → razón genérica en español, nunca el error crudo", func(t *testing.T) {
		failed := importBarcodeFailedRow(db, "tenant-csv", 8, "0000000000000", "otro-id", raceErr)
		require.NotNil(t, failed)
		assert.Contains(t, failed.Reason, "código de barras")
		assert.NotContains(t, failed.Reason, "SQLSTATE")
	})
	t.Run("otro error no se mapea (el caller conserva su manejo)", func(t *testing.T) {
		assert.Nil(t, importBarcodeFailedRow(db, "tenant-csv", 9, "7701234567890", "otro-id",
			errors.New("connection refused")))
	})
}
