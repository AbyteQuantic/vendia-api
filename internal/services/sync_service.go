package services

import (
	"encoding/json"
	"strings"
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
		return s.syncProduct(tx, tenantID, op)
	case "sale":
		return s.syncSale(tx, tenantID, op)
	case "customer":
		return s.syncEntity(tx, &models.Customer{}, tenantID, op)
	case "credit_account":
		return s.syncEntity(tx, &models.CreditAccount{}, tenantID, op)
	case "credit_payment":
		return s.syncCreditPayment(tx, tenantID, op)
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
	// Spec 095 — variantes de producto. Case EXPLÍCITO a propósito: sin
	// esto, la op cae al default de abajo (se marca "aplicada" sin tocar la
	// base — el mismo patrón que ya perdió datos 2 veces en esta sesión con
	// otras entidades).
	case "product_variant_group":
		return s.syncEntity(tx, &models.ProductVariantGroup{}, tenantID, op)
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

	// Art. III — un id que ya vive en OTRO tenant jamás se toca: sin este
	// guard, el Save de abajo sobreescribía la fila ajena y SetIdentity le
	// re-estampaba el tenant del atacante (los ids de eventos circulan en
	// landings públicas). Se trata como conflicto no aplicado.
	var foreign int64
	if err := tx.Model(dst).Where("id = ? AND tenant_id <> ?", op.ID, tenantID).
		Count(&foreign).Error; err == nil && foreign > 0 {
		return false, nil
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

// syncProduct — Spec 100 / D1: el camino `product` respeta el invariante "un
// código de barras = UN producto por tenant" sin envenenar el lote. Dos
// defectos reales que corrige frente al índice único parcial:
//   - create usaba ON CONFLICT DO NOTHING SIN target → la violación de
//     barcode se absorbía y el producto se descartaba EN SILENCIO
//     (applied=true, fila inexistente): cliente desincronizado para siempre.
//   - update (applyLWW) no clasificaba la violación → el error tumbaba la
//     transacción del lote COMPLETO → 500 → reintento infinito del lote
//     envenenado (mismo patrón del bug de syncSale documentado abajo).
//
// Un barcode duplicado se trata como conflicto NO aplicado (false, nil) —
// el mismo tratamiento que un LWW perdido: el lote sigue, el cliente ve el
// conteo en `conflicts` y el pull posterior le trae la verdad del servidor.
func (s *SyncService) syncProduct(tx *gorm.DB, tenantID string, op SyncOperation) (bool, error) {
	if op.Action != "delete" {
		if barcode := syncProductBarcode(op); barcode != "" {
			if FindBarcodeOwner(tx, tenantID, barcode, op.ID) != nil {
				return false, nil
			}
		}
	}
	applied, err := s.syncProductWrite(tx, tenantID, op)
	if err != nil && IsProductBarcodeUniqueViolation(err) {
		// Carrera entre lotes concurrentes: el índice detuvo la escritura.
		return false, nil
	}
	// Spec 104 — el sync escribe desde MAPAS (op.Data): el hook BeforeSave
	// del modelo no corre. Re-evaluar el léxico sobre la fila real.
	if applied && err == nil && op.Action != "delete" {
		EnsureProductModeration(tx, tenantID, op.ID)
	}
	return applied, err
}

// syncProductWrite espeja syncEntity para `product`, con UNA diferencia en
// create: el ON CONFLICT va acotado a (id) — la idempotencia por UUID sigue
// siendo silenciosa (Art. II), pero la violación del índice de barcode SÍ
// emerge como error clasificable en vez de absorberse en silencio.
func (s *SyncService) syncProductWrite(tx *gorm.DB, tenantID string, op SyncOperation) (bool, error) {
	if op.Action != "create" {
		return s.syncEntity(tx, &models.Product{}, tenantID, op)
	}
	// Data nil (cliente viejo/corrupto) → jamás un panic que envenene el
	// lote; conflicto no aplicado y el lote sigue (patrón syncEventScan).
	if op.Data == nil {
		return false, nil
	}
	// Art. III — el lookup del re-sync idempotente va SIEMPRE con tenant:
	// sin el filtro, un create re-enviado con el id de un producto AJENO
	// (los UUID circulan en el catálogo público) caía en applyLWW y
	// sobreescribía la fila del otro tenant. Si el id existe bajo otro
	// tenant, el Create de abajo choca contra products_pkey y el ON
	// CONFLICT (id) DO NOTHING lo absorbe sin tocar la fila ajena.
	model := &models.Product{}
	if err := tx.Where("id = ? AND tenant_id = ?", op.ID, tenantID).
		First(model).Error; err == nil {
		return s.applyLWW(tx, model, op)
	}
	op.Data["id"] = op.ID
	op.Data["tenant_id"] = tenantID
	err := tx.Model(model).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}}, DoNothing: true,
	}).Create(op.Data).Error
	return err == nil, err
}

// syncProductBarcode extrae el barcode del payload de la op (trim incluido:
// " 777 " y "777" son el mismo código, igual que en CreateProduct).
func syncProductBarcode(op SyncOperation) string {
	if op.Data == nil {
		return ""
	}
	if b, ok := op.Data["barcode"].(string); ok {
		return strings.TrimSpace(b)
	}
	return ""
}

func (s *SyncService) syncEntity(tx *gorm.DB, model any, tenantID string, op SyncOperation) (bool, error) {
	switch op.Action {
	case "create":
		// Data nil → jamás un panic que envenene el lote (ver syncProductWrite).
		if op.Data == nil {
			return false, nil
		}
		// Art. III — mismo guard que syncProductWrite: el lookup idempotente
		// filtra por tenant para que un id AJENO nunca caiga en applyLWW; la
		// colisión de pkey con la fila ajena la absorbe el ON CONFLICT DO
		// NOTHING sin escribir nada.
		result := tx.Where("id = ? AND tenant_id = ?", op.ID, tenantID).First(model)
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

	// Art. III — la identidad de la fila NO es reescribible por el payload
	// del cliente: sin este filtro, un update podía mover el producto a otro
	// tenant ("tenant_id" en op.Data) o re-apuntar su id. Copia nueva (no se
	// muta op.Data — otros caminos la estampan después).
	updates := make(map[string]any, len(op.Data))
	for k, v := range op.Data {
		if k == "id" || k == "tenant_id" {
			continue
		}
		updates[k] = v
	}
	if len(updates) == 0 {
		return true, nil
	}
	return true, tx.Model(model).Where("id = ?", op.ID).Updates(updates).Error
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

// syncCreditPayment inserta un abono offline idempotente. Art. III: la cuenta
// de fiado debe pertenecer al tenant del JWT — CreditPayment no tiene columna
// tenant_id, su pertenencia se hereda de credit_accounts, así que sin esta
// verificación un cliente autenticado de otro tenant podía "abonar" (falsear)
// la deuda de una tienda ajena con solo conocer el id de la cuenta.
func (s *SyncService) syncCreditPayment(tx *gorm.DB, tenantID string, op SyncOperation) (bool, error) {
	if op.Action != "create" || op.Data == nil {
		return false, nil
	}
	accountID, _ := op.Data["credit_account_id"].(string)
	if accountID == "" {
		return false, nil
	}
	var account models.CreditAccount
	if err := tx.Where("id = ? AND tenant_id = ?", accountID, tenantID).
		First(&account).Error; err != nil {
		// Cuenta ajena o inexistente → conflicto no aplicado, jamás se escribe.
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
