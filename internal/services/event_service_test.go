// Spec: specs/042-modulo-eventos/spec.md
package services_test

import (
	"testing"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupEventDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Event{},
		&models.EventRegistration{},
		&models.EventScan{},
	))
	return db
}

func validEvent(tenantID string) *models.Event {
	return &models.Event{
		Type:     models.EventTypeCurso,
		Title:    "Curso de repostería",
		Modality: models.EventModalityPresencial,
		Capacity: 20,
		Price:    100000,
	}
}

func TestEventService_CreateAndGet(t *testing.T) {
	db := setupEventDB(t)
	svc := services.NewEventService(db)

	created, err := svc.Create("tenant-a", validEvent("tenant-a"))
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, "tenant-a", created.TenantID)
	assert.Equal(t, models.EventStatusBorrador, created.Status)

	got, err := svc.Get("tenant-a", created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
}

func TestEventService_Create_RejectsInvalid(t *testing.T) {
	db := setupEventDB(t)
	svc := services.NewEventService(db)

	bad := validEvent("tenant-a")
	bad.Price = 12345 // not a multiple of $50
	_, err := svc.Create("tenant-a", bad)
	assert.Error(t, err)
}

func TestEventService_TenantIsolation(t *testing.T) {
	db := setupEventDB(t)
	svc := services.NewEventService(db)

	created, err := svc.Create("tenant-a", validEvent("tenant-a"))
	require.NoError(t, err)

	// tenant-b must not be able to read tenant-a's event.
	_, err = svc.Get("tenant-b", created.ID)
	assert.Error(t, err)

	list, err := svc.List("tenant-b", "")
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestEventService_PublishAndArchive(t *testing.T) {
	db := setupEventDB(t)
	svc := services.NewEventService(db)

	created, err := svc.Create("tenant-a", validEvent("tenant-a"))
	require.NoError(t, err)

	pub, err := svc.Publish("tenant-a", created.ID)
	require.NoError(t, err)
	assert.Equal(t, models.EventStatusPublicado, pub.Status)

	arch, err := svc.Archive("tenant-a", created.ID)
	require.NoError(t, err)
	assert.Equal(t, models.EventStatusArchivado, arch.Status)
}

func TestEventService_ListFiltersByStatus(t *testing.T) {
	db := setupEventDB(t)
	svc := services.NewEventService(db)

	draft, err := svc.Create("tenant-a", validEvent("tenant-a"))
	require.NoError(t, err)
	pubEv, err := svc.Create("tenant-a", validEvent("tenant-a"))
	require.NoError(t, err)
	_, err = svc.Publish("tenant-a", pubEv.ID)
	require.NoError(t, err)

	published, err := svc.List("tenant-a", models.EventStatusPublicado)
	require.NoError(t, err)
	require.Len(t, published, 1)
	assert.Equal(t, pubEv.ID, published[0].ID)

	all, err := svc.List("tenant-a", "")
	require.NoError(t, err)
	assert.Len(t, all, 2)
	_ = draft
}
