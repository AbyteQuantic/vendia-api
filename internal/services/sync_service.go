package services

import (
	"time"
	"vendia-backend/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SyncOperation struct {
	Entity          string         `json:"entity" binding:"required"`
	Action          string         `json:"action" binding:"required"`
	ID              string         `json:"id" binding:"required"`
	Data            map[string]any `json:"data"`
	ClientUpdatedAt time.Time      `json:"client_updated_at" binding:"required"`
}

type SyncRequest struct {
	Operations []SyncOperation `json:"operations" binding:"required"`
	LastSyncAt time.Time       `json:"last_sync_at"`
}

type ServerChange struct {
	Entity string `json:"entity"`
	ID     string `json:"id"`
	Action string `json:"action"`
	Data   any    `json:"data"`
}

type SyncResponse struct {
	Synced        int            `json:"synced"`
	Conflicts     int            `json:"conflicts"`
	ServerChanges []ServerChange `json:"server_changes"`
	SyncTimestamp time.Time      `json:"sync_timestamp"`
}

type SyncService struct {
	db *gorm.DB
}

func NewSyncService(db *gorm.DB) *SyncService {
	return &SyncService{db: db}
}

func (s *SyncService) ProcessBatch(tenantID string, req SyncRequest) (*SyncResponse, error) {
	now := time.Now()
	synced := 0
	conflicts := 0

	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, op := range req.Operations {
			applied, err := s.processOperation(tx, tenantID, op)
			if err != nil {
				return err
			}
			if applied {
				synced++
			} else {
				conflicts++
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	serverChanges := s.collectServerChanges(tenantID, req.LastSyncAt)

	s.db.Model(&models.Tenant{}).Where("id = ?", tenantID).Updates(map[string]any{
		"last_sync_at":     now,
		"pending_sync_ops": 0,
	})

	return &SyncResponse{
		Synced:        synced,
		Conflicts:     conflicts,
		ServerChanges: serverChanges,
		SyncTimestamp: now,
	}, nil
}

func (s *SyncService) processOperation(tx *gorm.DB, tenantID string, op SyncOperation) (bool, error) {
	switch op.Entity {
	case "product":
		return s.syncEntity(tx, &models.Product{}, tenantID, op)
	case "sale":
		return s.syncEntity(tx, &models.Sale{}, tenantID, op)
	case "customer":
		return s.syncEntity(tx, &models.Customer{}, tenantID, op)
	case "credit_account":
		return s.syncEntity(tx, &models.CreditAccount{}, tenantID, op)
	case "credit_payment":
		return s.syncCreditPayment(tx, op)
	default:
		return true, nil
	}
}

func (s *SyncService) syncEntity(tx *gorm.DB, model any, tenantID string, op SyncOperation) (bool, error) {
	switch op.Action {
	case "create":
		result := tx.Where("id = ?", op.ID).First(model)
		if result.Error == nil {
			return s.applyLWW(tx, model, op)
		}
		op.Data["id"] = op.ID
		op.Data["tenant_id"] = tenantID
		return true, tx.Model(model).Clauses(clause.OnConflict{DoNothing: true}).Create(op.Data).Error
	case "update":
		if err := tx.Where("id = ? AND tenant_id = ?", op.ID, tenantID).First(model).Error; err != nil {
			return false, nil
		}
		return s.applyLWW(tx, model, op)
	case "delete":
		if err := tx.Where("id = ? AND tenant_id = ?", op.ID, tenantID).First(model).Error; err != nil {
			return false, nil
		}
		return true, tx.Where("id = ?", op.ID).Delete(model).Error
	}
	return false, nil
}

func (s *SyncService) applyLWW(tx *gorm.DB, model any, op SyncOperation) (bool, error) {
	type hasUpdatedAt interface {
		GetUpdatedAt() time.Time
	}

	var serverUpdatedAt time.Time
	row := tx.Model(model).Where("id = ?", op.ID).Select("updated_at").Row()
	if err := row.Scan(&serverUpdatedAt); err != nil {
		return false, err
	}

	if op.ClientUpdatedAt.Before(serverUpdatedAt) {
		return false, nil
	}

	return true, tx.Model(model).Where("id = ?", op.ID).Updates(op.Data).Error
}

func applyUpdate(tx *gorm.DB, model any, op SyncOperation) (bool, error) {
	var serverUpdatedAt time.Time
	row := tx.Model(model).Where("id = ?", op.ID).Select("updated_at").Row()
	if err := row.Scan(&serverUpdatedAt); err != nil {
		return false, err
	}

	if op.ClientUpdatedAt.Before(serverUpdatedAt) {
		return false, nil
	}

	return true, tx.Model(model).Where("id = ?", op.ID).Updates(op.Data).Error
}

func (s *SyncService) syncCreditPayment(tx *gorm.DB, op SyncOperation) (bool, error) {
	if op.Action != "create" {
		return false, nil
	}
	op.Data["id"] = op.ID
	return true, tx.Model(&models.CreditPayment{}).Clauses(clause.OnConflict{DoNothing: true}).Create(op.Data).Error
}

func (s *SyncService) collectServerChanges(tenantID string, since time.Time) []ServerChange {
	var changes []ServerChange

	var products []models.Product
	s.db.Unscoped().Where("tenant_id = ? AND updated_at > ?", tenantID, since).Find(&products)
	for _, p := range products {
		action := "update"
		if p.DeletedAt.Valid {
			action = "delete"
		}
		changes = append(changes, ServerChange{Entity: "product", ID: p.ID, Action: action, Data: p})
	}

	var sales []models.Sale
	s.db.Unscoped().Preload("Items").Where("tenant_id = ? AND updated_at > ?", tenantID, since).Find(&sales)
	for _, sl := range sales {
		action := "update"
		if sl.DeletedAt.Valid {
			action = "delete"
		}
		changes = append(changes, ServerChange{Entity: "sale", ID: sl.ID, Action: action, Data: sl})
	}

	var customers []models.Customer
	s.db.Unscoped().Where("tenant_id = ? AND updated_at > ?", tenantID, since).Find(&customers)
	for _, cu := range customers {
		action := "update"
		if cu.DeletedAt.Valid {
			action = "delete"
		}
		changes = append(changes, ServerChange{Entity: "customer", ID: cu.ID, Action: action, Data: cu})
	}

	var credits []models.CreditAccount
	s.db.Unscoped().Where("tenant_id = ? AND updated_at > ?", tenantID, since).Find(&credits)
	for _, cr := range credits {
		action := "update"
		if cr.DeletedAt.Valid {
			action = "delete"
		}
		changes = append(changes, ServerChange{Entity: "credit_account", ID: cr.ID, Action: action, Data: cr})
	}

	return changes
}
