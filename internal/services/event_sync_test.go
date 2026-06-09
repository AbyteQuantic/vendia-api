// Spec: specs/042-modulo-eventos/spec.md
package services_test

import (
	"testing"
	"time"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupSyncDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Tenant{}, &models.Event{}, &models.EventRegistration{},
		&models.EventScan{}, &models.Customer{},
	))
	require.NoError(t, db.Create(&models.Tenant{
		BaseModel: models.BaseModel{ID: "tenant-a"}, OwnerName: "Org", Phone: "3000000000",
	}).Error)
	return db
}

func TestSync_CreatesEventAndRegistration(t *testing.T) {
	db := setupSyncDB(t)
	svc := services.NewSyncService(db)
	now := time.Now().UTC()

	resp, err := svc.ProcessBatch("tenant-a", services.SyncRequest{
		Operations: []services.SyncOperation{
			{
				Entity: "event", Action: "create",
				ID:              "e0000000-0000-4000-8000-000000000001",
				ClientUpdatedAt: now,
				Data: map[string]any{
					"type": "curso", "title": "Sync Curso", "modality": "virtual",
					"price": 50000, "capacity": 10, "status": "publicado",
				},
			},
			{
				Entity: "event_registration", Action: "create",
				ID:              "1ace0000-0000-4000-8000-000000000001",
				ClientUpdatedAt: now,
				Data: map[string]any{
					"event_id": "e0000000-0000-4000-8000-000000000001",
					"customer_id": "c1", "payment_status": "confirmed",
					"qr_token":     "aaaaaaaa-0000-4000-8000-000000000001",
					"public_token": "bbbbbbbb-0000-4000-8000-000000000001",
				},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, resp.Synced)

	var events, regs int64
	require.NoError(t, db.Model(&models.Event{}).Where("tenant_id = ?", "tenant-a").Count(&events).Error)
	require.NoError(t, db.Model(&models.EventRegistration{}).Where("tenant_id = ?", "tenant-a").Count(&regs).Error)
	assert.Equal(t, int64(1), events)
	assert.Equal(t, int64(1), regs)
}

func TestSync_EventScanNoDoubleCount(t *testing.T) {
	db := setupSyncDB(t)

	// Seed a registration the scans belong to.
	require.NoError(t, db.Create(&models.EventRegistration{
		BaseModel: models.BaseModel{ID: "1ace0000-0000-4000-8000-000000000002"},
		TenantID:  "tenant-a", EventID: "e2", CustomerID: "c1",
		QRToken: "aaaaaaaa-0000-4000-8000-000000000002", PublicToken: "bbbbbbbb-0000-4000-8000-000000000002",
		PaymentStatus: models.RegistrationPaymentConfirmed,
	}).Error)

	svc := services.NewSyncService(db)
	now := time.Now().UTC()

	// Two devices sync the SAME entrada scan (same registration+session+type)
	// with DIFFERENT client UUIDs. Only one row must survive (AC-11, R-03).
	scanOp := func(id string) services.SyncOperation {
		return services.SyncOperation{
			Entity: "event_scan", Action: "create", ID: id, ClientUpdatedAt: now,
			Data: map[string]any{
				"registration_id": "1ace0000-0000-4000-8000-000000000002",
				"session_index":   0, "scan_type": "in", "scanned_at": now,
			},
		}
	}
	_, err := svc.ProcessBatch("tenant-a", services.SyncRequest{
		Operations: []services.SyncOperation{scanOp("5ca00000-0000-4000-8000-000000000001")},
	})
	require.NoError(t, err)
	_, err = svc.ProcessBatch("tenant-a", services.SyncRequest{
		Operations: []services.SyncOperation{scanOp("5ca00000-0000-4000-8000-000000000002")},
	})
	require.NoError(t, err)

	var scans int64
	require.NoError(t, db.Model(&models.EventScan{}).
		Where("registration_id = ? AND scan_type = ?", "1ace0000-0000-4000-8000-000000000002", "in").
		Count(&scans).Error)
	assert.Equal(t, int64(1), scans, "el mismo escaneo desde dos dispositivos no debe duplicarse")
}
