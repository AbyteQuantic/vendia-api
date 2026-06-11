// Spec (parcial): specs/024-captcha-registro-login/spec.md — backend opt-in
// Spec (parcial): specs/025-captcha-pedidos-publicos/spec.md — extensión a rutas públicas
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"vendia-backend/internal/config"
	"vendia-backend/internal/database"
	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/services"
	"vendia-backend/internal/services/email"
	"vendia-backend/internal/services/push"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func main() {
	cfg := config.Load()

	db, err := database.Connect(cfg)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}

	if err := database.Migrate(db); err != nil {
		log.Fatalf("failed to run migrations: %v", err)
	}

	// Seed the super-admin row from env vars (no-op when unset).
	// Must run AFTER migrations so admin_users exists.
	if err := database.BootstrapSuperAdmin(db, database.BootstrapSuperAdminConfig{
		Email:    cfg.SeedAdminEmail,
		Password: cfg.SeedAdminPassword,
		Name:     cfg.SeedAdminName,
	}); err != nil {
		log.Printf("[BOOTSTRAP] super-admin seed failed: %v", err)
	}

	// Feature 014 self-heal: backfill NULL branch_id on the core
	// operational tables (products, sales, inventory_movements,
	// credit_accounts, order_tickets) so sede-scoped reads don't hide a
	// product/sale that was created without a sede claim. Idempotent —
	// subsequent boots are no-ops.
	if touched, err := database.BackfillBranchIDs(db); err != nil {
		log.Printf("[BOOTSTRAP] branch_id backfill failed: %v", err)
	} else if touched > 0 {
		log.Printf("[BOOTSTRAP] branch_id backfill repaired %d rows", touched)
	}
	// Backfill the remaining branch-scoped tables (user_workspaces,
	// credit_payments) — must run AFTER BackfillBranchIDs so the parent
	// credit_accounts are already scoped.
	database.BackfillRelatedBranchIDs(db)

	// Self-heal: every tenant must have at least the "Efectivo"
	// payment method seeded. Pre-fix tenants registered before the
	// seed landed and would otherwise render zero chips on the POS.
	// Idempotent — only touches tenants with zero methods.
	if err := database.SeedDefaultPaymentMethods(db); err != nil {
		log.Printf("[BOOTSTRAP] default payment-method seed failed: %v", err)
	}

	// Feature 008 self-heal: every tenant must have a subscription
	// row. Pre-008 registrations never created one (the DB trigger
	// never fired under AutoMigrate-only deploys), stranding those
	// tenants behind the soft paywall. This backfills a courtesy
	// 14-day TRIAL for any tenant missing a row. Idempotent — a
	// tenant that already has a subscription is left untouched.
	if created, err := database.SeedTenantSubscriptions(db); err != nil {
		log.Printf("[BOOTSTRAP] tenant-subscription backfill failed: %v", err)
	} else if created > 0 {
		log.Printf("[BOOTSTRAP] tenant-subscription backfill seeded %d trials", created)
	}

	// Spec F036 self-heal: mark every tenant that existed before the
	// F036 deploy as onboarding_completed=true so an established
	// business never gets the first-run wizard. One-shot — guarded by a
	// BootstrapMarker row, so subsequent boots are no-ops and a brand-new
	// post-deploy tenant keeps onboarding_completed=false.
	if touched, err := database.BackfillOnboardingCompleted(db); err != nil {
		log.Printf("[BOOTSTRAP] onboarding backfill failed: %v", err)
	} else if touched > 0 {
		log.Printf("[BOOTSTRAP] onboarding backfill marked %d pre-F036 tenants", touched)
	}

	// Spec F037 self-heal: F037 reclassifies several modules from
	// byType→opt-in (Marketing Hub, Recetas, Insumos, Trabajos de
	// Muebles, Órdenes de Compra). Without this backfill, every tenant
	// who was already using one of those modules would see it disappear
	// from the Dashboard the moment F037 deploys. Five one-shot
	// backfills run here in sequence, each guarded by its own
	// BootstrapMarker — a tenant with at least one row in the matching
	// source table gets its enable_* flag flipped to true. Subsequent
	// boots are no-ops.
	if touched, err := database.BackfillF037Capabilities(db); err != nil {
		log.Printf("[BOOTSTRAP] F037 capability backfill failed: %v", err)
	} else if touched > 0 {
		log.Printf("[BOOTSTRAP] F037 capability backfill flipped %d tenant×flags", touched)
	}

	// Spec F041 — siembra el catálogo dinámico (módulos + tipos + relaciones)
	// con paridad del catálogo actual. Idempotente: si ya hay módulos, no-op.
	// Permite gestionar el dashboard desde el admin sin releases; el
	// comportamiento inicial es idéntico al de hoy (AC-11).
	if created, err := database.SeedBusinessCatalog(db); err != nil {
		log.Printf("[BOOTSTRAP] catalog seed failed: %v", err)
	} else if created > 0 {
		log.Printf("[BOOTSTRAP] catalog seed inserted %d modules", created)
	}
	// F042 — top-up idempotente del módulo Eventos para deploys ya sembrados.
	if err := database.BackfillEventsCatalogModule(db); err != nil {
		log.Printf("[BOOTSTRAP] eventos catalog backfill failed: %v", err)
	}
	// F042 — contabiliza las inscripciones confirmadas (de pago) como ventas
	// del negocio (canal "Eventos"). Run-every-boot e idempotente: solo crea la
	// venta de inscripciones que aún no la tienen. Pone al día las confirmadas
	// antes de este deploy y repara cualquier venta no contabilizada en vivo.
	if created, err := database.BackfillEventSales(db); err != nil {
		log.Printf("[BOOTSTRAP] event sales backfill failed: %v", err)
	} else if created > 0 {
		log.Printf("[BOOTSTRAP] event sales backfill creó %d ventas de eventos", created)
	}

	// ── Initialize external services (optional, nil-safe) ───────────────────
	var geminiSvc *services.GeminiService
	if cfg.GeminiAPIKey != "" {
		geminiSvc = services.NewGeminiService(cfg.GeminiAPIKey, cfg.GeminiModel, cfg.GeminiImageModel, 30*time.Second).WithUsageDB(db)
		log.Println("[SVC] Gemini service initialized (AI usage → ai_usage_logs)")
	}

	// Storage: prefer Supabase Storage, fallback to R2
	var storageSvc services.FileStorage
	if cfg.SupabaseURL != "" && cfg.SupabaseServiceKey != "" {
		storageSvc = services.NewStorageService(cfg.SupabaseURL, cfg.SupabaseServiceKey)
		log.Println("[SVC] Supabase Storage service initialized")
	} else if cfg.R2AccountID != "" && cfg.R2AccessKeyID != "" && cfg.R2SecretKey != "" {
		r2, err := services.NewR2Service(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretKey, cfg.R2PublicURL)
		if err != nil {
			log.Printf("[SVC] warning: R2 init failed: %v", err)
		} else {
			storageSvc = r2
			log.Println("[SVC] Cloudflare R2 service initialized")
		}
	}

	// ── Push Notifications (Spec 038) ────────────────────────────────────────
	// UnifiedSender soporta FCM (Android/Chrome/Firefox) y Web Push
	// nativo (iOS Safari). Si AMBAS env vars están vacías, arrancamos
	// con FakeSender — el server vive, las rutas /devices/* funcionan,
	// pero las push reales no salen.
	var pushSender push.Sender
	unified, err := push.NewUnifiedSender(context.Background(),
		push.FCMConfig{
			ServiceAccountJSON: cfg.FCMServiceAccountJSON,
			ProjectID:          cfg.FCMProjectID,
		},
		push.VAPIDConfig{
			PublicKey:  cfg.VAPIDPublicKey,
			PrivateKey: cfg.VAPIDPrivateKey,
			Subject:    cfg.VAPIDSubject,
		},
	)
	if err != nil {
		pushSender = &push.FakeSender{}
		log.Printf("[PUSH] no backend configured (%v) — using FakeSender", err)
	} else {
		pushSender = unified
		fcmReady := cfg.FCMServiceAccountJSON != ""
		vapidReady := cfg.VAPIDPublicKey != ""
		log.Printf("[PUSH] UnifiedSender ready (FCM=%v, WebPush=%v)", fcmReady, vapidReady)
	}
	pushDispatcher := push.NewDispatcher(pushSender)

	// Email sender for event reminders (Spec F042). Degrades to a
	// FakeSender (logs only) when SMTP_* env vars are absent.
	emailSvc := email.NewService(email.Config{
		Host:     os.Getenv("SMTP_HOST"),
		Port:     os.Getenv("SMTP_PORT"),
		Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     os.Getenv("SMTP_FROM"),
	})
	if emailSvc.IsConfigured() {
		log.Println("[SVC] Email service initialized (SMTP)")
	}

	offSvc := services.NewOpenFoodFactsService()
	catalogCacheSvc := services.NewCatalogCacheService(db, offSvc)
	catalogCacheSvc.StartDailyRefresh(context.Background())
	log.Println("[SVC] Catalog cache service initialized (daily refresh active)")

	catalogSvc := services.NewCatalogService(db, storageSvc)
	catalogSvc.StartCleanupTicker(context.Background())

	// Daily self-heal: scan products for URLs whose bucket file is gone and
	// clear them so the UI can show "generate photo" instead of a broken image.
	// Loud regression alarm if image loss ever happens again.
	imageReconciler := services.NewImageReconciler(db, cfg.SupabaseURL)
	imageReconciler.StartDailyReconcile(context.Background())

	// Daily bucket mirror: copies product-photos and logo buckets into
	// matching *-backup buckets so accidental wipes stay recoverable for at
	// least one cycle. No-op if Supabase creds are missing.
	if cfg.SupabaseURL != "" && cfg.SupabaseServiceKey != "" {
		backupSvc := services.NewBackupService(cfg.SupabaseURL, cfg.SupabaseServiceKey)
		backupSvc.StartDailyBackup(context.Background())
	}

	itunesSvc := services.NewITunesService()

	// ePayco payment gateway (Feature 008). Always constructed —
	// IsConfigured() gates behaviour when credentials are absent, so
	// the subscription handlers never have to nil-check it.
	epaycoSvc := services.NewEpaycoService(services.EpaycoConfig{
		PublicKey:  cfg.EpaycoPublicKey,
		PrivateKey: cfg.EpaycoPrivateKey,
		PCustID:    cfg.EpaycoPCustID,
		PKey:       cfg.EpaycoPKey,
		TestMode:   cfg.EpaycoTestMode,
	})
	if epaycoSvc.IsConfigured() {
		log.Printf("[SVC] ePayco gateway initialized (test_mode=%v)", cfg.EpaycoTestMode)
	} else {
		log.Println("[SVC] ePayco gateway NOT configured — subscription checkout disabled")
	}

	// ── Captcha Cloudflare Turnstile (opt-in — F024) ────────────────────────
	// El middleware solo se registra si TURNSTILE_ENABLED=true Y
	// TURNSTILE_SECRET_KEY está presente. Default OFF para no romper
	// producción hasta que Bryan active las claves (FR-08, AC-09, D4).
	var captchaMiddleware gin.HandlerFunc
	turnstileEnabled := strings.EqualFold(strings.TrimSpace(os.Getenv("TURNSTILE_ENABLED")), "true")
	turnstileSecretKey := strings.TrimSpace(os.Getenv("TURNSTILE_SECRET_KEY"))
	turnstileVerifyURL := strings.TrimSpace(os.Getenv("TURNSTILE_VERIFY_URL"))
	if turnstileVerifyURL == "" {
		turnstileVerifyURL = services.TurnstileVerifyURL
	}

	if turnstileEnabled {
		if turnstileSecretKey == "" {
			log.Fatal("[CAPTCHA] TURNSTILE_ENABLED=true pero TURNSTILE_SECRET_KEY está vacío — configuración inválida, corregir antes de arrancar")
		}
		turnstileSvc := services.NewTurnstileService(
			turnstileSecretKey,
			turnstileVerifyURL,
			&http.Client{Timeout: 5 * time.Second},
		)
		captchaMiddleware = middleware.CaptchaMiddleware(turnstileSvc)
		log.Println("[CAPTCHA] Cloudflare Turnstile activado en /login, /api/v1/tenant/register y rutas de pedido público (F024+F025)")
	} else {
		log.Println("[CAPTCHA] deshabilitado (TURNSTILE_ENABLED=false) — endpoints sin captcha")
	}

	// F025 — rate-limit dedicado de 5 pedidos / 15 min / IP para las rutas
	// de pedido público. Activo SIEMPRE (independiente de TURNSTILE_ENABLED)
	// como defensa en capa (FR-02, AC-04, D4).
	orderRateLimiter := middleware.NewRateLimiter(5, 15*time.Minute)

	// ── Gin setup ───────────────────────────────────────────────────────────
	r := gin.New()

	r.Use(middleware.RequestLogger())
	r.Use(gin.Recovery())

	r.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.AllowedOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Authorization", "Content-Type", "X-Tenant-Override"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	loginLimiter := middleware.NewRateLimiter(cfg.RateLimitLogin, 1*time.Minute)
	globalLimiter := middleware.NewRateLimiter(100, 1*time.Minute)

	// ── Public routes ────────────────────────────────────────────────────────
	r.GET("/ping", handlers.Ping)

	// F024: si el captcha está habilitado, el middleware se inserta entre
	// el rate-limiter y el handler. En modo deshabilitado captchaMiddleware
	// es nil y buildHandlers lo omite transparentemente.
	r.POST("/login", buildHandlers(loginLimiter, captchaMiddleware, handlers.Login(db, cfg.JWTSecret))...)
	// Admin login lives on its own path so the tenant login rate
	// limiter, credentials table, and claim shape stay separate.
	r.POST("/api/v1/admin/login",
		loginLimiter, handlers.AdminLogin(db, cfg.JWTSecret))
	r.POST("/api/v1/tenant/register",
		buildHandlers(captchaMiddleware, handlers.TenantRegister(db, cfg.JWTSecret))...)

	// Onboarding logo preview — public, rate-limited. Lets the
	// merchant generate / upload their logo BEFORE registration so
	// the URL ships in the register payload. Without this the
	// onboarding logo step (5/6) was a "coming after Crear cuenta"
	// dead end. loginLimiter is reused (5/min/IP) — strict because
	// the IA call is the API's most expensive operation.
	r.POST("/api/v1/auth/preview-logo",
		loginLimiter, handlers.PreviewLogoIA(geminiSvc, storageSvc))
	r.POST("/api/v1/auth/preview-logo-upload",
		loginLimiter, handlers.PreviewLogoUpload(storageSvc))

	r.POST("/api/v1/auth/refresh", handlers.RefreshToken(db, cfg.JWTSecret))
	r.POST("/api/v1/auth/select-workspace", middleware.Auth(cfg.JWTSecret), handlers.SelectWorkspace(db, cfg.JWTSecret))

	// Subscription — PUBLIC endpoints (Feature 008). The ePayco
	// confirmation webhook MUST be public (ePayco posts to it from its
	// own servers, no JWT), so the handler verifies the SHA-256
	// signature itself. The response landing is a UX-only HTML page
	// shown after the checkout closes — it decides nothing.
	r.POST("/api/v1/subscription/epayco/confirmation",
		handlers.EpaycoConfirmation(db, epaycoSvc))
	r.GET("/api/v1/subscription/response", handlers.SubscriptionResponse())
	// Subscription checkout page (Feature 008). PUBLIC: the browser tab
	// the Flutter CTA opens via launchUrl carries no JWT. The handler
	// serves an HTML page that loads the ePayco widget for the {ref}
	// created by POST /subscription/checkout. The reference is
	// unguessable and the page exposes only the PUBLIC ePayco key.
	r.GET("/api/v1/subscription/pay/:ref",
		handlers.SubscriptionPayPage(db, epaycoSvc))

	// Public store / catalog (no auth required)
	r.GET("/api/v1/store/:slug/catalog", handlers.PublicCatalog(db))
	r.GET("/api/v1/store/:slug/product/:uuid", handlers.PublicProductDetail(db))
	// F025: rate-limit dedicado 5/15min/IP + captcha (cuando TURNSTILE_ENABLED).
	// GET /order/:uuid queda libre — solo los POSTs de creación son el vector
	// de abuso. (spec §2, FR-02, AC-04, FR-08)
	r.POST("/api/v1/store/:slug/order",
		buildHandlers(orderRateLimiter, captchaMiddleware, handlers.CreateWebOrder(db))...)
	r.GET("/api/v1/store/:slug/order/:uuid", handlers.GetWebOrderStatus(db))

	// Events public storefront (Spec F042). Listing/detail are open reads;
	// the inscription POST carries the dedicated rate-limiter + Turnstile
	// (F025) because it materializes a Customer and consumes resources.
	r.GET("/api/v1/store/:slug/events", handlers.PublicListEvents(db))
	r.GET("/api/v1/store/:slug/events/:id", handlers.PublicGetEvent(db))
	// Recupera la inscripción del asistente por teléfono (para mostrar su
	// componente en el catálogo sin el deep link del correo).
	r.POST("/api/v1/store/:slug/my-event-registration", handlers.PublicFindRegistration(db))
	// Tasa de cambio USD→COP para convertir el precio del evento al cambiar moneda.
	r.GET("/api/v1/fx/usd-cop", handlers.ExchangeRateUSDCOP())
	// Carné del asistente (por public_token): el QR solo viaja si ya pagó.
	r.GET("/api/v1/store/:slug/carnet/:token", handlers.PublicGetCarnet(db))
	// Comprobante de pago manual del asistente (queda pendiente de aprobación).
	r.POST("/api/v1/store/:slug/carnet/:token/proof", handlers.PublicSubmitPaymentProof(db, storageSvc))
	r.POST("/api/v1/store/:slug/events/:id/register",
		buildHandlers(orderRateLimiter, captchaMiddleware, handlers.PublicRegisterEvent(db))...)

	// Public rockola (customer suggests song)
	r.POST("/api/v1/rockola/:slug/suggest", handlers.SuggestSong(db))

	// Public account (customer sees their bill)
	r.GET("/api/v1/account/:order_uuid", handlers.GetAccountHTTP(db))
	r.POST("/api/v1/account/:order_uuid/verify", handlers.VerifyAccountPhone(db))

	// Public fiado handshake (customer accepts debt)
	r.GET("/api/v1/public/fiado/:token", handlers.GetFiadoPublic(db))
	r.POST("/api/v1/public/fiado/:token/accept", handlers.AcceptFiado(db))

	// Spec F031 — public quote link (customer views + approves/rejects).
	// No JWT: the unguessable public_token is the only credential, same
	// pattern as the public fiado handshake. Rate-limited with the same
	// dedicated 5/15min/IP limiter the public-order routes use, so a
	// scraper cannot enumerate tokens or spam the decide endpoint.
	r.GET("/api/v1/public/quotes/:token",
		orderRateLimiter, handlers.GetPublicQuote(db))
	r.POST("/api/v1/public/quotes/:token/decide",
		orderRateLimiter, handlers.DecidePublicQuote(db))

	// Spec F031 — internal cron endpoint for the expire-quotes batch job.
	// No JWT (Render Cron carries no tenant token); gated by a shared
	// Bearer secret read from CRON_TOKEN inside the handler.
	r.POST("/api/v1/internal/jobs/expire-quotes", handlers.ExpireQuotesJob(db))

	// Spec F033 — public broadcast-promotion link (customer views the
	// promo + tracks a visit). No JWT: the unguessable public_token is
	// the only credential, same pattern as the public quote/fiado links.
	// Rate-limited with the dedicated 5/15min/IP limiter so a scraper
	// cannot enumerate tokens or spam the visit endpoint.
	r.GET("/api/v1/public/broadcast-promotions/:token",
		orderRateLimiter, handlers.GetPublicBroadcastPromotion(db))
	r.POST("/api/v1/public/broadcast-promotions/:token/visit",
		orderRateLimiter, handlers.VisitPublicBroadcastPromotion(db))

	// Spec F033 — internal cron endpoint for the promotions-push batch
	// job (runs every 5 min). Same auth model as expire-quotes: no JWT,
	// gated by the shared CRON_TOKEN Bearer secret.
	r.POST("/api/v1/internal/jobs/promotions-push", handlers.PromotionsPushJob(db, pushDispatcher))

	// F042: recordatorios de eventos (cuotas + evento próximo). Diario.
	r.POST("/api/v1/internal/jobs/event-reminders", handlers.EventRemindersJob(db, pushDispatcher, emailSvc))

	// Public online orders (customer places order from catalog).
	// Two paths hit the same handler: the legacy shape and the
	// brief's KDS-Phase-1 naming. Keeping both means older admin-web
	// deploys still work while new clients migrate to `/catalog/.../orders`.
	// F025: rate-limit dedicado 5/15min/IP (always-on) + captchaMiddleware
	// cuando TURNSTILE_ENABLED=true. El orderRateLimiter va primero para
	// rechazar IPs abusivas antes de llamar a Cloudflare. (FR-02, AC-04, D4)
	r.POST("/api/v1/store/:slug/online-order",
		buildHandlers(orderRateLimiter, captchaMiddleware, handlers.PublicCreateOnlineOrder(db, pushDispatcher))...)
	r.POST("/api/v1/public/catalog/:slug/orders",
		buildHandlers(orderRateLimiter, captchaMiddleware, handlers.PublicCreateOnlineOrder(db, pushDispatcher))...)

	// Privacy-safe customer lookup for the checkout UI. Accepts a
	// phone number and returns ONLY {"needs_consent": bool} — never
	// the customer's name or any other PII. See
	// handlers/customer_consent.go for the security rationale.
	r.POST("/api/v1/public/catalog/:slug/check-customer", handlers.CheckCustomerConsent(db))
	// Customer Portal — sanitized order history by phone. The handler
	// scopes the lookup to the slug's tenant + strips PII before
	// returning, see PublicCustomerOrders for the privacy contract.
	r.GET("/api/v1/public/catalog/:slug/my-orders", handlers.PublicCustomerOrders(db))

	// Public "live tab" viewer. The QR printed for each table
	// encodes only the session_token (not the order id or tenant
	// id), so the lookup surface is narrow and un-guessable. See
	// handlers/table_sessions.go for the full security rationale.
	r.GET("/api/v1/public/table-sessions/:session_token", handlers.GetPublicTableSession(db))
	r.POST("/api/v1/public/table-sessions/:session_token/call-waiter", handlers.CallWaiter(db))
	// Customer-submitted abono from the QR page — lands as PENDING
	// until the tendero confirms on the POS. See handler for rationale.
	r.POST("/api/v1/public/table-sessions/:session_token/payments", handlers.SubmitPartialPayment(db))
	// Public receipt upload (screenshot of the transfer). Returns the
	// public URL the SubmitPartialPayment call attaches as receipt_url.
	r.POST("/api/v1/public/table-sessions/:session_token/receipts", handlers.UploadPaymentReceipt(db, storageSvc))

	// ── Protected routes (JWT) ───────────────────────────────────────────────
	v1 := r.Group("/api/v1")
	v1.Use(globalLimiter)
	v1.Use(middleware.Auth(cfg.JWTSecret))
	{
		v1.POST("/auth/logout", handlers.Logout(db))
		v1.GET("/auth/workspaces", handlers.ListWorkspaces(db))

		// Subscription — JWT endpoints (Feature 008). The catalogue,
		// the tenant's current state, and the checkout builder. The
		// ePayco confirmation webhook is mounted PUBLIC above.
		v1.GET("/subscription/plans", handlers.GetSubscriptionPlans())
		v1.GET("/subscription/status", handlers.GetSubscriptionStatus(db))

		// Spec F041 — catálogo dinámico de módulos/tipos para el dashboard.
		// Solo lectura; la app lo cachea (offline-first) y resuelve qué ver.
		v1.GET("/catalog", handlers.GetBusinessCatalog(db))
		v1.POST("/subscription/checkout", handlers.CreateSubscriptionCheckout(db, epaycoSvc))

		// Sync (offline-first)
		v1.POST("/sync/batch", handlers.SyncBatch(db))

		// Dashboard
		v1.GET("/sales/today", handlers.TodayStats(db))

		// Employees
		v1.GET("/employees", handlers.ListEmployees(db))
		v1.POST("/employees", handlers.CreateEmployee(db))
		v1.PATCH("/employees/:uuid", handlers.UpdateEmployee(db))
		v1.DELETE("/employees/:uuid", handlers.DeleteEmployee(db))
		v1.POST("/employees/verify-pin", handlers.VerifyPin(db))
		// Owner-only credential reset — also upserts the User +
		// UserWorkspace rows so the staff can log in via the
		// multi-tenant flow. See handler for the
		// "phone-belongs-to-multiple-tenants" rationale.
		v1.POST("/employees/:uuid/password", handlers.SetEmployeePassword(db))
		// Profile photo upload — Feature 019. Stores the tendero's
		// (owner = Employee with is_owner=true) or an employee's
		// avatar in the profile-photos bucket and persists the URL on
		// the Employee row. Tenant-scoped lookup before any write.
		v1.POST("/employees/:uuid/photo", handlers.UploadEmployeePhoto(db, storageSvc))

		// Branches (Phase 5 — multi-sede). List + read endpoints sit
		// here under tenant auth; CREATE lives in the premium group
		// below so a second sede requires PRO / TRIAL.
		v1.GET("/store/branches", handlers.ListBranches(db))
		v1.PATCH("/store/branches/:id", handlers.UpdateBranch(db))
		v1.DELETE("/store/branches/:id", handlers.DeleteBranch(db))

		// Products
		v1.GET("/products", handlers.ListProducts(db))
		v1.GET("/products/by-barcode", handlers.LookupProductByBarcode(db))
		v1.POST("/products", handlers.CreateProduct(db, catalogSvc))
		v1.PATCH("/products/:id", handlers.UpdateProduct(db, catalogSvc))
		v1.POST("/products/:id/restock", handlers.RestockProduct(db))
		v1.DELETE("/products/:id", handlers.DeleteProduct(db))
		v1.POST("/products/seed", middleware.DevOnly(cfg.Env), handlers.SeedProducts(db))
		// F027 — bulk import from Excel/CSV wizard (Flutter + admin-web).
		// No captcha — endpoint is authenticated (JWT required, handled by v1 group).
		// God-mode: super_admin + X-Tenant-Override header → import for any tenant.
		v1.POST("/products/import", handlers.ImportProducts(db))
		v1.GET("/products/lookup", handlers.LookupBarcode(offSvc))
		v1.GET("/products/search-off", handlers.SearchProductsOFF(catalogCacheSvc))
		v1.GET("/products/catalog-sync", handlers.CatalogDump(db))
		v1.GET("/products/pending-prices", handlers.PendingPrices(db))
		v1.PATCH("/products/:id/price", handlers.SetProductPrice(db))
		v1.POST("/products/:id/photo", handlers.UploadProductPhoto(db, storageSvc))
		v1.POST("/products/:id/enhance", handlers.EnhanceProductPhoto(db, geminiSvc, storageSvc, catalogSvc))
		v1.POST("/products/:id/generate-image", handlers.GenerateProductImage(db, geminiSvc, storageSvc, catalogSvc))
		// Feature 016 — async AI photo polling. The enhance/generate
		// endpoints above now answer 202 with a job_id; the client
		// polls this endpoint for the result. Tenant-scoped (Art. III).
		v1.GET("/products/:id/ai-job/:jobId", handlers.GetAIJob(db))

		// Catalog
		v1.GET("/catalog/search", handlers.SearchCatalog(catalogSvc, catalogCacheSvc))
		v1.GET("/catalog/:id/images", handlers.GetCatalogImages(catalogSvc))
		v1.POST("/catalog/images/:image_id/accept", handlers.AcceptCatalogImage(catalogSvc))

		// Inventory IA
		v1.POST("/inventory/scan-invoice", handlers.ScanInvoice(db, geminiSvc, offSvc))
		v1.GET("/inventory/alerts", handlers.InventoryAlerts(db))
		v1.GET("/inventory/expiring", handlers.ExpiringProducts(db))
		// Kardex & Smart Dedup
		v1.GET("/inventory/kardex", handlers.ProductKardex(db))
		v1.GET("/inventory/report", handlers.InventoryReport(db))
		v1.POST("/inventory/match-products", handlers.MatchProductsHandler(db))
		v1.GET("/inventory/reorder-suggestions", handlers.ReorderSuggestions(db))
		v1.POST("/inventory/invoice-logs", handlers.LogInvoiceSave(db))
		v1.GET("/inventory/invoice-logs", handlers.ListInvoiceLogs(db))

		// Sales (POS)
		v1.POST("/sales", handlers.CreateSale(db, pushDispatcher))
		v1.GET("/sales", handlers.ListSales(db))
		v1.GET("/sales/history", handlers.SalesHistory(db))
		v1.GET("/sales/:uuid/receipt", handlers.SaleReceipt(db))
		v1.POST("/sales/:uuid/reprint", handlers.ReprintReceipt(db))
		v1.POST("/sales/:uuid/send-receipt", handlers.SendReceipt(db))

		// Customers
		v1.GET("/customers", handlers.ListCustomers(db))
		v1.POST("/customers", handlers.CreateCustomer(db))
		v1.PATCH("/customers/:id", handlers.UpdateCustomer(db))
		// F030 — per-customer purchase history (summary + paged sales).
		v1.GET("/customers/:id/history", handlers.GetCustomerHistory(db))
		// F026 — bulk import from Excel/CSV wizard (Flutter + admin-web).
		// No captcha — endpoint is authenticated (JWT required, handled by v1 group).
		// God-mode: super_admin + X-Tenant-Override header → import for any tenant.
		v1.POST("/customers/import", handlers.ImportCustomers(db))

		// Quotes (Spec F031 — cotizaciones). CRUD + lifecycle actions.
		// All tenant-scoped via the JWT (Constitución Art. III).
		v1.GET("/quotes", handlers.ListQuotes(db))
		v1.POST("/quotes", handlers.CreateQuote(db))
		v1.GET("/quotes/:id", handlers.GetQuote(db))
		v1.PATCH("/quotes/:id", handlers.UpdateQuote(db))
		v1.DELETE("/quotes/:id", handlers.DeleteQuote(db))
		v1.POST("/quotes/:id/send", handlers.SendQuote(db))
		v1.POST("/quotes/:id/mark-status", handlers.MarkQuoteStatus(db))
		v1.POST("/quotes/:id/convert", handlers.ConvertQuote(db))

		// Events (Spec F042) — organizer-side CRUD + publish. Attendee
		// inscription, payment and check-in live on the public group below.
		v1.GET("/events", handlers.ListEvents(db))
		v1.POST("/events", handlers.CreateEvent(db))
		v1.GET("/events/:id", handlers.GetEvent(db))
		v1.PATCH("/events/:id", handlers.UpdateEvent(db))
		v1.DELETE("/events/:id", handlers.DeleteEvent(db))
		v1.POST("/events/:id/publish", handlers.PublishEvent(db))
		v1.POST("/events/:id/checkin", handlers.CheckinEvent(db))
		v1.GET("/events/:id/registrations", handlers.ListEventRegistrations(db))
		v1.GET("/events/:id/registrations/export", handlers.ExportEventRegistrations(db))
		v1.POST("/events/:id/registrations/:rid/certificate", handlers.IssueCertificate(db))
		// Envío masivo de certificados a quienes registraron entrada y salida.
		v1.POST("/events/:id/certificates/issue-all", handlers.IssueAllCertificates(db))
		// Diseñador de certificado: guarda solo el config (texto/firma/logo/layout).
		v1.PUT("/events/:id/certificate-config", handlers.UpdateEventCertificateConfig(db))
		// Pagos de la inscripción (abonos/cuotas + marcar pagado) — F042.
		v1.POST("/events/:id/registrations/:rid/payments", handlers.RecordRegistrationPayment(db))
		v1.POST("/events/:id/registrations/:rid/confirm-payment", handlers.ConfirmRegistrationPayment(db))
		// Mapa de sillas: asignar / mover / liberar la silla de un asistente.
		v1.PUT("/events/:id/registrations/:rid/seat", handlers.AssignRegistrationSeat(db))
		// Comprobantes manuales: bandeja de revisión + aprobar (activa carné).
		v1.GET("/events/:id/payments", handlers.ListEventPayments(db))
		v1.POST("/events/:id/payments/:pid/approve", handlers.ApproveEventPayment(db))
		v1.POST("/events/:id/badge/ai-generate", handlers.GenerateEventBadgeImage(db, geminiSvc, storageSvc))
		v1.POST("/events/:id/certificate/ai-generate", handlers.GenerateEventCertificateImage(db, geminiSvc, storageSvc))
		v1.POST("/events/:id/poster/ai-generate", handlers.GenerateEventPosterImage(db, geminiSvc, storageSvc))
		// "Sube tu propia imagen" — alternativa a la IA para cada pieza (FR-11/13).
		v1.POST("/events/:id/badge/upload", handlers.UploadEventBadgeImage(db, storageSvc))
		v1.POST("/events/:id/certificate/upload", handlers.UploadEventCertificateImage(db, storageSvc))
		v1.POST("/events/:id/poster/upload", handlers.UploadEventPosterImage(db, storageSvc))
		// QR de un medio de pago → devuelve la URL para payment_details (sirve
		// al crear y al editar; ruta propia para no chocar con /events/:id).
		v1.POST("/event-payment-qr", handlers.UploadEventPaymentQR(storageSvc))
		// Agente de IA que redacta la descripción del evento.
		v1.POST("/event-description-ai", handlers.GenerateEventDescriptionAI(geminiSvc))
		// Agente de IA que redacta los textos del certificado (editables).
		v1.POST("/event-certificate-texts-ai", handlers.GenerateEventCertificateTexts(db, geminiSvc))
		// Limpia con IA la foto de la firma para el certificado.
		v1.POST("/event-signature-clean", handlers.CleanEventSignature(geminiSvc, storageSvc))
		// Quita solo el fondo del logo (flood-fill, sin tocar blancos internos).
		v1.POST("/event-logo-remove-bg", handlers.RemoveEventLogoBackground(storageSvc))
		// "Mejorar con IA" — retoca la imagen actual (subida o generada).
		v1.POST("/events/:id/badge/ai-enhance", handlers.GenerateEventBadgeEnhance(db, geminiSvc, storageSvc))
		v1.POST("/events/:id/certificate/ai-enhance", handlers.GenerateEventCertificateEnhance(db, geminiSvc, storageSvc))
		v1.POST("/events/:id/poster/ai-enhance", handlers.GenerateEventPosterEnhance(db, geminiSvc, storageSvc))

		// Credits (El Fiar)
		v1.GET("/credits", handlers.ListCredits(db))
		v1.POST("/credits", handlers.CreateCredit(db))
		v1.GET("/credits/:id", handlers.GetCredit(db))
		v1.POST("/credits/:id/payments", handlers.CreatePayment(db, pushDispatcher))
		// Append to an already-accepted open account (no handshake needed)
		v1.POST("/credits/:id/append", handlers.AppendToFiado(db))
		// Close a fiado manually — write off any residual balance
		v1.POST("/credits/:id/close", handlers.CloseCredit(db, pushDispatcher))
		// Cancel a pending fiado — restores stock + voids linked sales
		v1.POST("/credits/:id/cancel", handlers.CancelCredit(db))

		// Dynamic QR for zero-fee Nequi/Daviplata/Bancolombia transfers
		v1.POST("/payments/generate-dynamic-qr", handlers.GenerateDynamicQR(db))

		// Fiado handshake (protected - init + check status)
		v1.POST("/fiado/init", handlers.InitFiado(db))
		v1.GET("/fiado/:token/status", handlers.CheckFiadoStatus(db))
		v1.POST("/fiar/remind/:customer_uuid", handlers.RemindCredit(db))

		// Tables (Floor Plan)
		v1.GET("/tables", handlers.ListTables(db))
		v1.POST("/tables", handlers.CreateTable(db))
		v1.PATCH("/tables/:id", handlers.UpdateTable(db))
		v1.POST("/tables/sync", handlers.SyncTables(db))

		// Table-tab upsert & lookup. Drives the "Open Tab" side of
		// the POS: persists the cashier's local cart as an
		// OrderTicket keyed by label so the live-tab QR has a
		// stable session_token across rounds. See handlers/
		// table_tabs.go for the full rationale.
		v1.PUT("/tables/tab", handlers.UpsertTableTab(db))
		v1.POST("/tables/tab/add-items", handlers.AddItemsToTableTab(db))
		v1.GET("/tables/tab/:label", handlers.GetTableTab(db))
		v1.DELETE("/orders/:uuid/items/:item_id", handlers.RemoveItemFromTab(db))

		// Notifications
		v1.GET("/notifications", handlers.ListNotifications(db))
		v1.POST("/notifications/read", handlers.MarkNotificationsRead(db))

		// Online orders (tenant management)
		v1.GET("/online-orders", handlers.ListOnlineOrders(db))
		v1.PATCH("/online-orders/:id", handlers.UpdateOnlineOrderStatus(db))

		// Store & Catalog Management
		v1.GET("/store/config", handlers.GetStoreConfig(db))
		v1.PATCH("/store/status", handlers.UpdateStoreStatus(db))
		v1.PATCH("/store/payment-config", handlers.UpdatePaymentConfig(db))
		v1.PATCH("/store/slug", handlers.UpdateStoreSlug(db))

		// Panic button
		v1.GET("/store/panic-config", handlers.GetPanicConfig(db))
		v1.PATCH("/store/panic-config", handlers.UpdatePanicMessage(db))
		v1.POST("/store/panic-config/contacts", handlers.CreateEmergencyContact(db))
		v1.DELETE("/store/panic-config/contacts/:id", handlers.DeleteEmergencyContact(db))
		v1.POST("/store/panic/trigger", handlers.TriggerPanic(db))

		// Tabs (Open accounts — legacy)
		v1.GET("/tabs", handlers.ListOpenTabs(db))
		v1.POST("/tabs", handlers.OpenTab(db))
		v1.PATCH("/tabs/:id/items", handlers.AddItemsToTab(db))
		v1.POST("/tabs/:id/close", handlers.CloseTab(db))

		// Orders / KDS (new order ticket system)
		v1.POST("/orders", handlers.CreateOrder(db))
		v1.GET("/orders", handlers.ListOrders(db))
		v1.GET("/orders/:uuid", handlers.GetOrder(db))
		v1.PATCH("/orders/:uuid/status", handlers.UpdateOrderStatus(db))
		v1.GET("/orders/open-accounts", handlers.OpenAccounts(db))
		v1.POST("/orders/:uuid/close", handlers.CloseOrder(db))
		// Live-tab partial payments — tendero registers a manual
		// abono (APPROVED direct) or lists the abonos on a ticket
		// so the POS TabReviewScreen can render them.
		v1.POST("/orders/partial-payments", handlers.RegisterPartialPayment(db))
		v1.GET("/orders/:uuid/partial-payments", handlers.ListPartialPayments(db))
		// Reverse-QR: staff scans the customer-side QR and this
		// flips the PENDING_SCAN abono to APPROVED, capturing the
		// employee responsible for the cash.
		v1.POST("/orders/payments/:payment_id/confirm", handlers.ConfirmPartialPayment(db))

		// Suppliers
		v1.GET("/suppliers", handlers.ListSuppliers(db))
		v1.POST("/suppliers", handlers.CreateSupplier(db))
		v1.PATCH("/suppliers/:uuid", handlers.UpdateSupplier(db))
		v1.DELETE("/suppliers/:uuid", handlers.DeleteSupplier(db))
		v1.POST("/suppliers/:uuid/order-wa", handlers.SupplierOrderWA(db))

		// Purchase orders (Feature 002 — órdenes de compra de insumos).
		// The literal /from-reorder route is registered before /:uuid so
		// the static path wins over the param match.
		v1.GET("/purchase-orders", handlers.ListPurchaseOrders(db))
		v1.POST("/purchase-orders", handlers.CreatePurchaseOrder(db))
		v1.POST("/purchase-orders/from-reorder", handlers.PurchaseOrdersFromReorder(db))
		v1.GET("/purchase-orders/:uuid", handlers.GetPurchaseOrder(db))
		v1.PATCH("/purchase-orders/:uuid", handlers.UpdatePurchaseOrder(db))
		v1.DELETE("/purchase-orders/:uuid", handlers.DeletePurchaseOrder(db))
		v1.POST("/purchase-orders/:uuid/send", handlers.SendPurchaseOrder(db))
		v1.POST("/purchase-orders/:uuid/receive", handlers.ReceivePurchaseOrderHandler(db))

		// Recipes / Insumos
		v1.GET("/recipes", handlers.ListRecipes(db))
		v1.POST("/recipes", handlers.CreateRecipe(db))
		v1.PATCH("/recipes/:uuid", handlers.UpdateRecipe(db))
		v1.DELETE("/recipes/:uuid", handlers.DeleteRecipe(db))
		v1.GET("/recipes/:uuid/cost", handlers.RecipeCost(db))
		// Feature 001 — units a product-receta can be made from the
		// current insumo stock (FR-05).
		v1.GET("/recipes/:uuid/availability", handlers.RecipeAvailability(db))

		// Insumos (Feature 001) — raw-material inventory CRUD. The
		// /low-stock route is registered before /:uuid so the literal
		// path wins over the param match.
		v1.GET("/ingredients", handlers.ListIngredients(db))
		v1.GET("/ingredients/low-stock", handlers.LowStockIngredients(db))
		v1.POST("/ingredients", handlers.CreateIngredient(db))
		v1.PATCH("/ingredients/:uuid", handlers.UpdateIngredient(db))
		v1.DELETE("/ingredients/:uuid", handlers.DeleteIngredient(db))

		// Work orders (Feature 003 — trabajos de fabricación y
		// reparación de muebles). CRUD + status transitions; a
		// transition to `terminada` discounts material stock via the
		// kardex. Anticipos and the WhatsApp quote share live on
		// their own sub-routes.
		v1.GET("/work-orders", handlers.ListWorkOrders(db))
		v1.POST("/work-orders", handlers.CreateWorkOrder(db))
		v1.GET("/work-orders/:uuid", handlers.GetWorkOrder(db))
		v1.PATCH("/work-orders/:uuid", handlers.UpdateWorkOrder(db))
		v1.DELETE("/work-orders/:uuid", handlers.DeleteWorkOrder(db))
		v1.POST("/work-orders/:uuid/payments", handlers.CreateWorkOrderPayment(db))
		v1.POST("/work-orders/:uuid/share", handlers.ShareWorkOrder(db))

		// Promotions
		v1.GET("/promotions", handlers.ListPromotions(db))
		v1.POST("/promotions", handlers.CreatePromotion(db))
		v1.PATCH("/promotions/:uuid", handlers.UpdatePromotion(db))
		v1.DELETE("/promotions/:uuid", handlers.DeletePromotion(db))
		v1.GET("/promotions/suggestions", handlers.PromotionSuggestions(db))
		v1.POST("/promotions/apply-to-pos", handlers.ApplyPromoToPOS(db))

		// Spec F033 — broadcast promotions module. CRUD + RFM audience
		// selector + assisted-queue deliveries. Deliberately under
		// /broadcast-promotions so there is no path collision with the
		// legacy combo /promotions routes above.
		v1.GET("/broadcast-promotions", handlers.ListBroadcastPromotions(db))
		v1.POST("/broadcast-promotions", handlers.CreateBroadcastPromotion(db))
		v1.GET("/broadcast-promotions/:id", handlers.GetBroadcastPromotion(db))
		v1.PATCH("/broadcast-promotions/:id", handlers.UpdateBroadcastPromotion(db))
		v1.DELETE("/broadcast-promotions/:id", handlers.DeleteBroadcastPromotion(db))
		v1.POST("/broadcast-promotions/:id/audience", handlers.BroadcastPromotionAudience(db))
		v1.POST("/broadcast-promotions/:id/deliveries", handlers.CreateBroadcastDeliveries(db))
		v1.PATCH("/broadcast-promotions/:id/deliveries/:deliveryId", handlers.UpdateBroadcastDelivery(db))
		v1.POST("/broadcast-promotions/upload-image", handlers.UploadBroadcastPromotionImage(db, storageSvc))

		// Marketing — AI banner generator (auth'd, rate-limited via global middleware)
		v1.POST("/marketing/generate-banner", handlers.GenerateMarketingBanner(geminiSvc, storageSvc))

		// Store config — PATCH lives in the Marketing Hub section;
		// GET is already bound above in "Store & Catalog Management".
		v1.PATCH("/store/config", handlers.UpdateStoreConfig(db))

		// Store slug — dedicated GET for the Marketing Hub. GET
		// auto-provisions a slug from the business name on first call.
		// PATCH /store/slug is already bound above.
		v1.GET("/store/slug", handlers.GetStoreSlug(db))

		// Business profile
		v1.GET("/store/profile", handlers.GetBusinessProfile(db))
		v1.PATCH("/store/profile", handlers.UpdateBusinessProfile(db))

		// VAT / Growth Radar (Safe Tax Flow epic) — backend mirror of the
		// Flutter TaxSettingsService (SharedPreferences). Hydrated on login.
		v1.GET("/tenant/vat", handlers.GetTenantVATSettings(db))
		v1.PATCH("/tenant/vat", handlers.UpdateTenantVATSettings(db))

		// Payment info (Nequi/Daviplata — legacy)
		v1.GET("/tenant/payment-info", handlers.GetPaymentInfo(db))
		v1.PATCH("/tenant/payment-info", handlers.UpdatePaymentInfo(db))
		v1.GET("/payments/qr", handlers.GeneratePaymentQR(db))

		// Payment methods (CRUD)
		v1.GET("/store/payment-methods", handlers.ListPaymentMethods(db))
		v1.POST("/store/payment-methods", handlers.CreatePaymentMethod(db))
		v1.PATCH("/store/payment-methods/:id", handlers.UpdatePaymentMethod(db))
		v1.DELETE("/store/payment-methods/:id", handlers.DeletePaymentMethod(db))
		// Multipart QR upload lives on its own sub-route so the
		// JSON-only POST above keeps its tight contract and clients
		// that don't need QRs don't pay the multipart parser tax.
		v1.POST("/store/payment-methods/:id/qr",
			handlers.UploadPaymentMethodQR(db, storageSvc))

		// Logo IA
		v1.POST("/tenant/generate-logo", handlers.GenerateLogo(db, geminiSvc, storageSvc))
		v1.POST("/tenant/upload-logo", handlers.UploadLogo(db, storageSvc))

		// Owner PIN — cashier handshake for restricted actions (new fiado, void, etc.)
		v1.POST("/tenant/owner-pin", handlers.SetOwnerPin(db))
		v1.POST("/tenant/owner-pin/verify", handlers.VerifyOwnerPin(db))

		// Analytics / Reportes
		v1.GET("/analytics/dashboard", handlers.AnalyticsDashboard(db))
		v1.GET("/analytics/top-products", handlers.TopProducts(db))
		v1.GET("/analytics/photo-coverage", handlers.PhotoCoverage(db))
		v1.GET("/analytics/sales-by-employee", handlers.SalesByEmployee(db))
		v1.GET("/analytics/inventory-health", handlers.InventoryHealth(db))
		v1.GET("/analytics/ingestion-method", handlers.IngestionMethod(db))
		v1.GET("/analytics/financial-summary", handlers.FinancialSummary(db))
		v1.GET("/analytics/sales-history", handlers.SalesHistoryByPeriod(db))
		v1.GET("/analytics/products-insights", handlers.ProductInsights(db))

		// Spec 038 — Push Notifications: registro y revocación de tokens.
		v1.POST("/devices/register", handlers.RegisterDevice(db))
		v1.GET("/devices/me", handlers.ListMyDevices(db))
		v1.DELETE("/devices/me/:id", handlers.RevokeMyDevice(db))
		v1.POST("/devices/me/test", handlers.TestPushSelf(db, pushDispatcher))

		// Rockola (admin)
		v1.GET("/rockola/pending", handlers.PendingSongs(db))
		v1.PATCH("/rockola/:uuid/played", handlers.MarkSongPlayed(db))
		v1.GET("/rockola/search", handlers.SearchSongs(itunesSvc))

		// OCR (legacy endpoint)
		v1.POST("/ocr/invoice", handlers.OCRInvoice(cfg.GeminiAPIKey))

		// Phase 3 — Support hub (tenant side)
		v1.POST("/support/tickets", handlers.CreateSupportTicket(db))
		v1.GET("/support/tickets", handlers.ListTenantTickets(db))
		v1.GET("/support/tickets/:id", handlers.GetTenantTicket(db))
		v1.POST("/support/tickets/:id/messages", handlers.AddTenantMessage(db))

		// Cart-session locks — surface which device is holding each
		// POS cart slot so multi-employee tenants don't race on the
		// same cuenta. See handlers/cart_sessions.go.
		v1.GET("/carts/sessions", handlers.ListCartSessions(db))
		v1.POST("/carts/sessions/claim", handlers.ClaimCartSession(db))
		v1.POST("/carts/sessions/heartbeat", handlers.HeartbeatCartSession(db))
		v1.POST("/carts/sessions/release", handlers.ReleaseCartSession(db))
	}

	// ── Premium routes (JWT + PremiumAuth: TRIAL activo o PRO_ACTIVE) ────────
	// Phase 4 — AI Voice-to-Catalog. The endpoint hits Gemini multimodal
	// and is the first gated feature on the SaaS roadmap; mounting here
	// puts the soft-paywall rail in production for real traffic.
	premium := r.Group("/api/v1")
	premium.Use(globalLimiter)
	premium.Use(middleware.Auth(cfg.JWTSecret))
	premium.Use(middleware.PremiumAuth(db))
	{
		premium.POST("/ai/voice-inventory", handlers.VoiceInventory(geminiSvc))
		// Creating a second sede is PRO-gated — FREE/PAST_DUE get the
		// same soft-paywall 403 the Flutter client already handles.
		premium.POST("/store/branches", handlers.CreateBranch(db))
	}

	// ── Admin routes (super_admin only) ──────────────────────────────────────
	admin := r.Group("/api/v1/admin")
	admin.Use(globalLimiter)
	admin.Use(middleware.Auth(cfg.JWTSecret))
	admin.Use(middleware.SuperAdminOnly())
	{
		// Hotfix 2026-05-31 — agregador con 6 secciones que la
		// pantalla /admin/analytics del admin-web consume.
		admin.GET("/analytics", handlers.AdminAnalytics(db))
		admin.GET("/analytics/overview", handlers.AdminOverview(db))
		admin.GET("/analytics/ai-costs", handlers.AdminAICosts(db))
		admin.GET("/analytics/revenue", handlers.AdminSubscriptionRevenue(db))
		admin.GET("/analytics/profitability", handlers.AdminProfitability(db, cfg.ProMonthlyPriceUSD))
		admin.GET("/tenants", handlers.AdminListTenants(db))
		admin.GET("/tenants/:id", handlers.AdminGetTenant(db))
		admin.PATCH("/tenants/:id/subscription", handlers.AdminUpdateSubscription(db))

		// Phase 2 — Ecosystem analyzer
		admin.GET("/ecosystem/cross-identities", handlers.AdminCrossIdentities(db))
		admin.GET("/ecosystem/metrics", handlers.AdminEcosystemMetrics(db))

		// Phase 3 — Support hub (super-admin side)
		admin.GET("/support/tickets", handlers.AdminListSupportTickets(db))
		admin.GET("/support/tickets/:id", handlers.AdminGetSupportTicket(db))
		admin.POST("/support/tickets/:id/messages", handlers.AdminAddTicketMessage(db))
		admin.PATCH("/support/tickets/:id/status", handlers.AdminUpdateSupportTicket(db))

		// Catalog CMS & Template Engine
		admin.GET("/catalogs/templates", handlers.AdminListCatalogTemplates(db))
		admin.POST("/catalogs/templates", handlers.AdminCreateCatalogTemplate(db))
		admin.PATCH("/catalogs/templates/:id", handlers.AdminUpdateCatalogTemplate(db))
		admin.DELETE("/catalogs/templates/:id", handlers.AdminDeleteCatalogTemplate(db))
		admin.GET("/catalogs/analytics", handlers.AdminGetCatalogAnalytics(db))

		// Spec 038 — Push broadcast manual (super_admin → un tenant).
		admin.POST("/push/broadcast", handlers.BroadcastPush(db, pushDispatcher))

		// Spec F041 — gestión del catálogo dinámico de módulos y tipos.
		admin.GET("/catalog/modules", handlers.AdminListBusinessModules(db))
		admin.POST("/catalog/modules", handlers.AdminCreateBusinessModule(db))
		// "modules-reorder" (no "/modules/reorder") para no chocar en el
		// árbol de rutas POST con el wildcard ":id" de /modules/:id/archive.
		admin.POST("/catalog/modules-reorder", handlers.AdminReorderBusinessModules(db))
		admin.PATCH("/catalog/modules/:id", handlers.AdminUpdateBusinessModule(db))
		admin.POST("/catalog/modules/:id/archive", handlers.AdminArchiveBusinessModule(db))
		admin.PUT("/catalog/modules/:id/relations", handlers.AdminSetModuleRelations(db))
		admin.GET("/catalog/business-types", handlers.AdminListBusinessTypes(db))
		admin.POST("/catalog/business-types", handlers.AdminCreateBusinessType(db))
		admin.PATCH("/catalog/business-types/:id", handlers.AdminUpdateBusinessType(db))
		admin.POST("/catalog/business-types/:id/archive", handlers.AdminArchiveBusinessType(db))
		admin.GET("/catalog/preview", handlers.AdminCatalogPreview(db))
		admin.GET("/catalog/audit-logs", handlers.AdminListCatalogAuditLogs(db))
		admin.GET("/tenants/:id/module-overrides", handlers.AdminListTenantOverrides(db))
		admin.PUT("/tenants/:id/module-overrides/:moduleId", handlers.AdminPutTenantOverride(db))
		admin.DELETE("/tenants/:id/module-overrides/:moduleId", handlers.AdminDeleteTenantOverride(db))
	}

	log.Printf("VendIA backend running on :%s (env=%s)", cfg.Port, cfg.Env)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}

// buildHandlers construye una lista de gin.HandlerFunc omitiendo los
// handlers nil. Permite insertar captchaMiddleware (nil cuando está
// deshabilitado) sin cambiar la firma de r.POST(). (F024, FR-08, D4).
func buildHandlers(handlers ...gin.HandlerFunc) []gin.HandlerFunc {
	result := make([]gin.HandlerFunc, 0, len(handlers))
	for _, h := range handlers {
		if h != nil {
			result = append(result, h)
		}
	}
	return result
}
