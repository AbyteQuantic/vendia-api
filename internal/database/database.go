package database

import (
	"database/sql"
	"fmt"
	"log"
	"time"
	"vendia-backend/internal/config"
	"vendia-backend/internal/models"

	"github.com/pressly/goose/v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Connect(cfg *config.Config) (*gorm.DB, error) {
	gormCfg := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	}

	var db *gorm.DB
	var err error
	for attempt := 1; attempt <= 5; attempt++ {
		db, err = gorm.Open(postgres.Open(cfg.DatabaseURL), gormCfg)
		if err == nil {
			break
		}
		log.Printf("[DB] attempt %d/5 failed: %v — retrying in %ds...", attempt, err, attempt*2)
		time.Sleep(time.Duration(attempt*2) * time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("could not connect to database after 5 attempts: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(5)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(4 * time.Minute)
	sqlDB.SetConnMaxIdleTime(90 * time.Second)

	log.Println("[DB] connection established")
	return db, nil
}

func Migrate(db *gorm.DB) error {
	log.Println("[DB] running auto-migrations...")

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	if _, err := sqlDB.Exec(`CREATE EXTENSION IF NOT EXISTS "pgcrypto"`); err != nil {
		log.Printf("[DB] warning: could not create pgcrypto extension: %v", err)
	}
	if _, err := sqlDB.Exec(`CREATE EXTENSION IF NOT EXISTS "pg_trgm"`); err != nil {
		log.Printf("[DB] warning: could not create pg_trgm extension: %v", err)
	}

	err = db.AutoMigrate(
		&models.Tenant{},
		&models.Employee{},
		&models.Product{},
		&models.ProductMedia{},       // Spec 070 — galería multimedia
		&models.SupplierOrder{},      // Spec 075 — pedido cross-tenant a proveedores
		&models.IngredientPrice{},    // Spec 077 — precios multi-fuente (append-only)
		&models.ChainPrice{},         // Spec 077 — catálogo scrapeado de cadenas
		&models.PurchaseErrand{},     // Spec 077 — mandados de compra
		&models.PurchaseErrandLine{}, // Spec 077 — líneas de mandado
		&models.TaskDismissal{},      // Spec 078 — snooze de tareas agregadas

		&models.Sale{},
		&models.SaleItem{},
		&models.RefreshToken{},
		&models.Customer{},
		&models.CreditAccount{},
		&models.CreditPayment{},
		&models.Table{},
		&models.OpenTab{},
		&models.AdminUser{},
		&models.Supplier{},
		&models.OrderTicket{},
		&models.OrderItem{},
		&models.Recipe{},
		&models.RecipeIngredient{},
		&models.Promotion{},
		&models.PromotionItem{},
		&models.RockolaSuggestion{},
		&models.CatalogProduct{},
		&models.CatalogImage{},
		&models.TenantPaymentMethod{},
		&models.EmergencyContact{},
		&models.SosAlert{},
		&models.SosAlertDelivery{},
		&models.Notification{},
		&models.OnlineOrder{},
		&models.User{},
		&models.UserWorkspace{},
		&models.Branch{},
		&models.TenantSubscription{},
		&models.SupportTicket{},
		// Spec 038/hotfix 2026-05-31: support_ticket_messages se omitió
		// en migración original — el handler AdminListSupportTickets
		// hace SELECT contra esta tabla y devolvía 500 en /admin/support.
		// Aditivo y retrocompatible (Art. X).
		&models.SupportTicketMessage{},
		// Catalog CMS (migration 024). Registered here so a zero-state
		// deploy doesn't 500 on GET /admin/catalogs/templates. The SQL
		// migration file stays as canonical reference but Render runs
		// AutoMigrate, not goose, so the structs are the source of truth.
		&models.CatalogTemplate{},
		&models.TenantCatalogConfig{},
		&models.CatalogAnalytics{},
		// Live-tab epic 2026-04-24: abonos / partial payments against
		// open table tickets so the customer can settle in parts from
		// the public QR page and the tendero can reconcile from the
		// POS tab-review screen.
		&models.PartialPayment{},
		&models.AIUsageLog{},
		&models.SubscriptionPayment{},
		// Feature 008 — persisted ePayco checkout: the bridge row
		// between POST /subscription/checkout and the backend-served
		// GET /subscription/pay/:ref page. Additive table (Art. X).
		&models.SubscriptionCheckout{},
		// Cart-session lock (2026-04-28): tracks who is currently
		// editing each POS cart slot so a second device can't
		// stomp the cashier's work in real time.
		&models.CartSession{},
		// Kardex — inventory movement log for full traceability.
		&models.InventoryMovement{},
		// Invoice scan audit trail for the owner.
		&models.InvoiceLog{},
		// Feature 001 — Ingredient (insumo) is raw-material inventory.
		// Additive: legacy clients keep selling direct products
		// unaffected (Art. X). Recipe/RecipeIngredient/Product columns
		// added by this feature are picked up by AutoMigrate on the
		// already-registered structs above.
		&models.Ingredient{},
		// Feature 002 — purchase orders. Two new tables; the new
		// kardex movement type `purchase_receipt` needs no migration
		// (it is a string value on the existing column). Additive and
		// backward-compatible (Art. X).
		&models.PurchaseOrder{},
		&models.PurchaseOrderItem{},
		// Feature 003 — furniture fabrication / repair work orders.
		// Three new tables; the new kardex movement type
		// `work_order_consumption` needs no migration (it is a string
		// value on the existing column). Additive and backward-
		// compatible (Art. X).
		&models.WorkOrder{},
		&models.WorkOrderItem{},
		&models.WorkOrderPayment{},
		// Feature 016 — asynchronous AI photo jobs. One new table that
		// tracks each enhance/generate operation so the client can poll
		// its status instead of holding a long synchronous request.
		// Additive and backward-compatible (Art. X).
		&models.AIJob{},
		// Spec F031 — quotes module. Three new tables (Quote, QuoteItem,
		// QuoteSequence) plus the EnableQuotes bool on tenants picked up
		// on the already-registered Tenant struct. Additive and
		// backward-compatible — zero legacy rows, no backfill (Art. X).
		&models.Quote{},
		&models.QuoteItem{},
		&models.QuoteSequence{},
		// Spec F033 — broadcast promotions module. Three new tables
		// (BroadcastPromotion, BroadcastPromotionItem,
		// BroadcastPromotionDelivery) plus the EnablePromotions bool on
		// tenants picked up on the already-registered Tenant struct.
		// Deliberately separate from the legacy combo-promo Promotion /
		// PromotionItem tables above — the carousel module is untouched.
		// Additive and backward-compatible — zero legacy rows, no
		// backfill (Art. X).
		&models.BroadcastPromotion{},
		&models.BroadcastPromotionItem{},
		&models.BroadcastPromotionDelivery{},
		// Spec F036 — dashboard adaptativo + onboarding. Adds the
		// OnboardingCompleted bool on the already-registered Tenant
		// struct and the BootstrapMarker table that guards the one-shot
		// onboarding backfill. Additive and backward-compatible (Art. X).
		&models.BootstrapMarker{},
		// Spec F038 — push notifications Fase 1 (Web + Android).
		// Tabla nueva que almacena los tokens FCM por usuario y
		// dispositivo. Aditiva y retrocompatible — los nuevos campos
		// opcionales en Notification (DeepLink, PushedAt, DedupKey)
		// son recogidos automáticamente por AutoMigrate sobre el
		// struct Notification ya registrado más arriba (Art. X).
		&models.DeviceToken{},
		// Spec F041 — catálogo dinámico de módulos y tipos de negocio.
		// Cinco tablas nuevas que mueven a la DB lo que estaba hardcodeado
		// en Flutter + Go, para gestión desde el admin sin releases.
		// Aditivo y retrocompatible: NO toca las columnas enable_* del
		// Tenant (conviven como estado activado por tienda — D1) (Art. X).
		&models.BusinessModule{},
		&models.BusinessTypeCatalog{},
		&models.ModuleTypeRelation{},
		&models.TenantModuleOverride{},
		&models.CatalogAuditLog{},
		// Spec F042 — módulo de eventos. Tres tablas nuevas (Event,
		// EventRegistration, EventScan) más el flag EnableEvents en el
		// FeatureFlags ya registrado sobre el Tenant. VendIA es solo el
		// puente: el dinero del asistente va directo al organizador, nunca
		// a VendIA. Aditivo y retrocompatible — cero filas legadas, sin
		// backfill (Art. X).
		&models.Event{},
		&models.EventRegistration{},
		&models.EventScan{},
		// F042 §12 D3 — cronograma de cuotas persistido con fechas.
		&models.EventInstallment{},
		// F042 — pagos/comprobantes manuales (sin pasarela): el invitado sube
		// el comprobante y el organizador lo aprueba para activar el carné.
		&models.EventPayment{},
		// Spec 066 — planear menú. Dos tablas nuevas (WeeklyMenuPlan por
		// tenant + MenuPlanOverride por fecha) que alimentan el menú
		// dinámico del link público. Aditivo y retrocompatible: sin plan,
		// el catálogo público se comporta como antes (Art. X).
		&models.WeeklyMenuPlan{},
		&models.MenuPlanOverride{},
	)
	if err != nil {
		return err
	}

	// ── Ledger constraints (Postgres-only; SQLite test driver no-ops) ──
	// Runs after AutoMigrate so the columns exist. Idempotent — uses
	// IF NOT EXISTS. Backfill MUST run BEFORE the unique index so
	// existing rows with denormalized phones don't fail the constraint.
	if IsPostgres(db) {
		if err := backfillNormalizedPhones(db); err != nil {
			log.Printf("[bootstrap] phone backfill: %v", err)
		}
		if err := applyLedgerIndexes(db); err != nil {
			log.Printf("[bootstrap] ledger indexes: %v", err)
		}
		// Audit 2026-06-24 — índices compuestos para las rutas calientes
		// (dashboard, /tasks, lista de productos, sync). Aditivos/idempotentes.
		if err := applyPerformanceIndexes(db); err != nil {
			log.Printf("[bootstrap] performance indexes: %v", err)
		}
		// Spec 083 — una sola cuenta de mesa ABIERTA por (tenant, label).
		if err := applyTableAccountIndex(db); err != nil {
			log.Printf("[bootstrap] table-account unique index: %v", err)
		}
		// F042 — el tipo academias_instituciones debe pasar el CHECK
		// tenants_business_types_valid. Render solo corre AutoMigrate, no
		// los .sql, así que actualizamos la función de validación en el
		// bootstrap (idempotente; CREATE OR REPLACE no toca el constraint).
		if err := ensureBusinessTypesWhitelist(db); err != nil {
			log.Printf("[bootstrap] business_types whitelist: %v", err)
		}
		// Spec 066 — el menú pasó de por-tenant a por-(tenant, sede). Los
		// índices únicos viejos de una sola columna bloquearían múltiples
		// sedes; AutoMigrate ya creó los compuestos, aquí soltamos los
		// viejos (idempotente). Render solo corre AutoMigrate, no .sql.
		if err := ensureMenuPlanIndexes(db); err != nil {
			log.Printf("[bootstrap] menu-plan indexes: %v", err)
		}
	}

	log.Println("[DB] auto-migrations completed")
	return nil
}

func RunGooseMigrations(sqlDB *sql.DB, migrationsDir string) error {
	log.Println("[DB] running goose migrations...")
	goose.SetBaseFS(nil)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}
	if err := goose.Up(sqlDB, migrationsDir); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	log.Println("[DB] goose migrations completed")
	return nil
}
