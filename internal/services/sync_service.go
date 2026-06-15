package services

import (
	"encoding/json"
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

// canonicalSyncEntity normaliza alias de entidad enviados por clientes viejos.
// Spec 047 AC-06: versiones previas del cliente encolaban offline `entity:
// "credit"`, que no existía en el switch y se descartaba silenciosamente
// (default → true,nil). Lo mapeamos a su nombre real para drenar esas ops.
func canonicalSyncEntity(entity string) string {
	if entity == "credit" {
		return "credit_account"
	}
	return entity
}

// isLegacyOfflineSalePayload reconoce el envoltorio de venta offline que los
// clientes viejos encolaban en /sync/batch (op.Data = localSale.toJson()). Esas
// llaves NO son columnas de `sales`; si llegan a syncEntity.Create(map) el
// INSERT revienta y envenena el lote. Cualquiera de estas llaves delata el
// payload legado — el insert real de Sale nunca las lleva.
func isLegacyOfflineSalePayload(data map[string]any) bool {
	if data == nil {
		return false
	}
	for _, k := range []string{"items", "uuid", "customer_uuid", "is_credit_sale", "sale_origin", "table_label"} {
		if _, ok := data[k]; ok {
			return true
		}
	}
	return false
}

func (s *SyncService) processOperation(tx *gorm.DB, tenantID string, op SyncOperation) (bool, error) {
	switch canonicalSyncEntity(op.Entity) {
	case "product":
		return s.syncEntity(tx, &models.Product{}, tenantID, op)
	case "sale":
		return s.syncSale(tx, tenantID, op)
	case "customer":
		return s.syncEntity(tx, &models.Customer{}, tenantID, op)
	case "credit_account":
		return s.syncEntity(tx, &models.CreditAccount{}, tenantID, op)
	case "credit_payment":
		return s.syncCreditPayment(tx, op)
	case "event":
		var ev models.Event
		return s.syncJSONEntity(tx, tenantID, op, &ev)
	case "event_registration":
		var reg models.EventRegistration
		return s.syncJSONEntity(tx, tenantID, op, &reg)
	case "event_scan":
		return s.syncEventScan(tx, tenantID, op)
	case "event_installment":
		var inst models.EventInstallment
		return s.syncJSONEntity(tx, tenantID, op, &inst)
	default:
		return true, nil
	}
}

// tenantScoped is implemented by event models so syncJSONEntity can stamp the
// authoritative tenant_id from the JWT regardless of the client payload.
type tenantScoped interface {
	SetIdentity(id, tenantID string)
}

// syncJSONEntity upserts an event-domain model that carries serializer:json
// columns. The generic syncEntity path uses map-based Create/Update, which
// bypasses GORM's JSON serializer (it falls back to a raw Scan and fails on
// slice/struct columns). Round-tripping op.Data through the struct makes the
// serializer apply on both write and read. Offline clients send full entity
// snapshots, so a timestamp-gated Save is the correct LWW semantic (Art. II).
func (s *SyncService) syncJSONEntity(tx *gorm.DB, tenantID string, op SyncOperation, dst tenantScoped) (bool, error) {
	if op.Action == "delete" {
		if err := tx.Where("id = ? AND tenant_id = ?", op.ID, tenantID).First(dst).Error; err != nil {
			return false, nil
		}
		return true, tx.Where("id = ?", op.ID).Delete(dst).Error
	}

	// Last-Write-Wins: discard a write older than the server's row.
	var serverUpdatedAt time.Time
	if err := tx.Model(dst).Where("id = ?", op.ID).Select("updated_at").Row().Scan(&serverUpdatedAt); err == nil {
		if op.ClientUpdatedAt.Before(serverUpdatedAt) {
			return false, nil
		}
	}

	raw, err := json.Marshal(op.Data)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, err
	}
	dst.SetIdentity(op.ID, tenantID)
	return true, tx.Save(dst).Error
}

// syncEventScan persists an offline check-in/out scan idempotently. The
// composite unique index (registration_id, session_index, scan_type) plus
// ON CONFLICT DO NOTHING means the same scan synced from two devices never
// double counts (Spec F042 AC-11, decision R-03). When a new scan lands, the
// owning registration's certificate eligibility is recomputed inside the same
// transaction.
func (s *SyncService) syncEventScan(tx *gorm.DB, tenantID string, op SyncOperation) (bool, error) {
	if op.Data == nil {
		return false, nil
	}
	op.Data["id"] = op.ID
	op.Data["tenant_id"] = tenantID
	res := tx.Model(&models.EventScan{}).Clauses(clause.OnConflict{DoNothing: true}).Create(op.Data)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected > 0 {
		if regID, ok := op.Data["registration_id"].(string); ok && regID != "" {
			// Best-effort: a failed recompute must not abort the batch.
			_ = NewEventCheckinService(tx).RecomputeEligibility(tenantID, regID)
		}
	}
	return res.RowsAffected > 0, nil
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

// syncSale persists a synced Sale and then explodes the recipe of any
// product-receta line it carries (Feature 001, FR-03). It delegates the
// row write to syncEntity and only adds the explosion on a freshly
// applied `create`. The explosion is idempotent by (sale_uuid,
// ingredient_id), so a sale that arrives twice through sync never
// double-discounts the insumos (Art. II). A direct-product sale is
// untouched — ExplodeRecipe is a no-op for non-recipe products (AC-06).
func (s *SyncService) syncSale(tx *gorm.DB, tenantID string, op SyncOperation) (bool, error) {
	// Spec 047 — las ventas viajan SOLO por POST /api/v1/sales (CreateSale,
	// idempotente por UUID: re-POST devuelve 200 sin doble descuento). El
	// camino /sync/batch para 'sale' quedó obsoleto y ROTO: clientes viejos
	// encolaban op.Data = localSale.toJson(), cuyas llaves
	// (uuid/customer_uuid/is_credit_sale/sale_origin/table_label/items) NO son
	// columnas de `sales`. syncEntity.Create(map) usa las llaves como columnas
	// → el INSERT falla → ProcessBatch hace rollback de TODO el lote (también
	// las ops de producto/cliente encoladas detrás) y el cliente reintenta para
	// siempre (replay real: HTTP 500). Mismo patrón ack-and-skip que el `default`
	// del switch (canonicalSyncEntity, Spec 047 AC-06): reconocemos la op como
	// aplicada (true,nil) para que el cliente la borre de su cola — la venta NO
	// se pierde: vive en LocalSale(synced=false) y sincroniza vía
	// getUnsyncedSales()→pushToServer()→/api/v1/sales. Esto drena las colas rotas
	// ya instaladas sin SQL ni migración (viven en el Isar del dispositivo).
	if isLegacyOfflineSalePayload(op.Data) {
		return true, nil
	}

	applied, err := s.syncEntity(tx, &models.Sale{}, tenantID, op)
	if err != nil || !applied {
		return applied, err
	}
	if op.Action != "create" {
		return applied, nil
	}

	// Re-load the persisted sale with its items so the explosion runs
	// against the authoritative server state, not the raw op payload.
	var sale models.Sale
	if err := tx.Preload("Items").
		Where("id = ? AND tenant_id = ?", op.ID, tenantID).
		First(&sale).Error; err != nil {
		// Sale not found (e.g. OnConflict DoNothing skipped it) — the
		// row already existed, nothing new to explode.
		return applied, nil
	}

	recipeSvc := NewRecipeService(s.db)
	for _, item := range sale.Items {
		if item.ProductID == nil || *item.ProductID == "" {
			continue
		}
		if err := recipeSvc.ExplodeRecipe(tx, ExplodeParams{
			TenantID:  tenantID,
			SaleUUID:  sale.ID,
			ProductID: *item.ProductID,
			Quantity:  item.Quantity,
			BranchID:  sale.BranchID,
			UserID:    sale.CreatedBy,
		}); err != nil {
			return false, err
		}
	}
	return applied, nil
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
