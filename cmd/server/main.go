package main

import (
	"context"
	"log"
	"time"
	"vendia-backend/internal/config"
	"vendia-backend/internal/database"
	"vendia-backend/internal/handlers"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/services"

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

	// Phase-6 self-heal: backfill NULL branch_id on legacy operational
	// rows so sede-scoped reads don't hide pre-Phase-5 inventory/sales.
	// Idempotent — subsequent boots are no-ops.
	database.BackfillBranchIDs(db)

	// ── Initialize external services (optional, nil-safe) ───────────────────
	var geminiSvc *services.GeminiService
	if cfg.GeminiAPIKey != "" {
		geminiSvc = services.NewGeminiService(cfg.GeminiAPIKey, cfg.GeminiModel, cfg.GeminiImageModel, 30*time.Second)
		log.Println("[SVC] Gemini service initialized")
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

	// ── Gin setup ───────────────────────────────────────────────────────────
	r := gin.New()

	r.Use(middleware.RequestLogger())
	r.Use(gin.Recovery())

	r.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.AllowedOrigins,
		AllowMethods:     []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Authorization", "Content-Type"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	loginLimiter := middleware.NewRateLimiter(cfg.RateLimitLogin, 1*time.Minute)
	globalLimiter := middleware.NewRateLimiter(100, 1*time.Minute)

	// ── Public routes ────────────────────────────────────────────────────────
	r.GET("/ping", handlers.Ping)
	r.POST("/login", loginLimiter, handlers.Login(db, cfg.JWTSecret))
	// Admin login lives on its own path so the tenant login rate
	// limiter, credentials table, and claim shape stay separate.
	r.POST("/api/v1/admin/login",
		loginLimiter, handlers.AdminLogin(db, cfg.JWTSecret))
	r.POST("/api/v1/tenant/register", handlers.TenantRegister(db, cfg.JWTSecret))
	r.POST("/api/v1/auth/refresh", handlers.RefreshToken(db, cfg.JWTSecret))
	r.POST("/api/v1/auth/select-workspace", middleware.Auth(cfg.JWTSecret), handlers.SelectWorkspace(db, cfg.JWTSecret))

	// Public store / catalog (no auth required)
	r.GET("/api/v1/store/:slug/catalog", handlers.PublicCatalog(db))
	r.GET("/api/v1/store/:slug/product/:uuid", handlers.PublicProductDetail(db))
	r.POST("/api/v1/store/:slug/order", handlers.CreateWebOrder(db))
	r.GET("/api/v1/store/:slug/order/:uuid", handlers.GetWebOrderStatus(db))

	// Public rockola (customer suggests song)
	r.POST("/api/v1/rockola/:slug/suggest", handlers.SuggestSong(db))

	// Public account (customer sees their bill)
	r.GET("/api/v1/account/:order_uuid", handlers.GetAccountHTTP(db))
	r.POST("/api/v1/account/:order_uuid/verify", handlers.VerifyAccountPhone(db))

	// Public fiado handshake (customer accepts debt)
	r.GET("/api/v1/public/fiado/:token", handlers.GetFiadoPublic(db))
	r.POST("/api/v1/public/fiado/:token/accept", handlers.AcceptFiado(db))

	// Public online orders (customer places order from catalog).
	// Two paths hit the same handler: the legacy shape and the
	// brief's KDS-Phase-1 naming. Keeping both means older admin-web
	// deploys still work while new clients migrate to `/catalog/.../orders`.
	r.POST("/api/v1/store/:slug/online-order", handlers.PublicCreateOnlineOrder(db))
	r.POST("/api/v1/public/catalog/:slug/orders", handlers.PublicCreateOnlineOrder(db))

	// Privacy-safe customer lookup for the checkout UI. Accepts a
	// phone number and returns ONLY {"needs_consent": bool} — never
	// the customer's name or any other PII. See
	// handlers/customer_consent.go for the security rationale.
	r.POST("/api/v1/public/catalog/:slug/check-customer", handlers.CheckCustomerConsent(db))

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

		// Branches (Phase 5 — multi-sede). List + read endpoints sit
		// here under tenant auth; CREATE lives in the premium group
		// below so a second sede requires PRO / TRIAL.
		v1.GET("/store/branches", handlers.ListBranches(db))
		v1.PATCH("/store/branches/:id", handlers.UpdateBranch(db))
		v1.DELETE("/store/branches/:id", handlers.DeleteBranch(db))

		// Products
		v1.GET("/products", handlers.ListProducts(db))
		v1.POST("/products", handlers.CreateProduct(db, catalogSvc))
		v1.PATCH("/products/:id", handlers.UpdateProduct(db, catalogSvc))
		v1.DELETE("/products/:id", handlers.DeleteProduct(db))
		v1.POST("/products/seed", middleware.DevOnly(cfg.Env), handlers.SeedProducts(db))
		v1.GET("/products/lookup", handlers.LookupBarcode(offSvc))
		v1.GET("/products/search-off", handlers.SearchProductsOFF(catalogCacheSvc))
		v1.GET("/products/catalog-sync", handlers.CatalogDump(db))
		v1.GET("/products/pending-prices", handlers.PendingPrices(db))
		v1.PATCH("/products/:id/price", handlers.SetProductPrice(db))
		v1.POST("/products/:id/photo", handlers.UploadProductPhoto(db, storageSvc))
		v1.POST("/products/:id/enhance", handlers.EnhanceProductPhoto(db, geminiSvc, storageSvc, catalogSvc))
		v1.POST("/products/:id/generate-image", handlers.GenerateProductImage(db, geminiSvc, storageSvc, catalogSvc))

		// Catalog
		v1.GET("/catalog/search", handlers.SearchCatalog(catalogSvc, catalogCacheSvc))
		v1.GET("/catalog/:id/images", handlers.GetCatalogImages(catalogSvc))
		v1.POST("/catalog/images/:image_id/accept", handlers.AcceptCatalogImage(catalogSvc))

		// Inventory IA
		v1.POST("/inventory/scan-invoice", handlers.ScanInvoice(db, geminiSvc, offSvc))
		v1.GET("/inventory/alerts", handlers.InventoryAlerts(db))
		v1.GET("/inventory/expiring", handlers.ExpiringProducts(db))

		// Sales (POS)
		v1.POST("/sales", handlers.CreateSale(db))
		v1.GET("/sales", handlers.ListSales(db))
		v1.GET("/sales/history", handlers.SalesHistory(db))
		v1.GET("/sales/:uuid/receipt", handlers.SaleReceipt(db))
		v1.POST("/sales/:uuid/reprint", handlers.ReprintReceipt(db))
		v1.POST("/sales/:uuid/send-receipt", handlers.SendReceipt(db))

		// Customers
		v1.GET("/customers", handlers.ListCustomers(db))
		v1.POST("/customers", handlers.CreateCustomer(db))
		v1.PATCH("/customers/:id", handlers.UpdateCustomer(db))

		// Credits (El Fiar)
		v1.GET("/credits", handlers.ListCredits(db))
		v1.POST("/credits", handlers.CreateCredit(db))
		v1.GET("/credits/:id", handlers.GetCredit(db))
		v1.POST("/credits/:id/payments", handlers.CreatePayment(db))
		// Append to an already-accepted open account (no handshake needed)
		v1.POST("/credits/:id/append", handlers.AppendToFiado(db))
		// Close a fiado manually — write off any residual balance
		v1.POST("/credits/:id/close", handlers.CloseCredit(db))
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
		v1.GET("/tables/tab/:label", handlers.GetTableTab(db))

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

		// Suppliers
		v1.GET("/suppliers", handlers.ListSuppliers(db))
		v1.POST("/suppliers", handlers.CreateSupplier(db))
		v1.PATCH("/suppliers/:uuid", handlers.UpdateSupplier(db))
		v1.DELETE("/suppliers/:uuid", handlers.DeleteSupplier(db))
		v1.POST("/suppliers/:uuid/order-wa", handlers.SupplierOrderWA(db))

		// Recipes / Insumos
		v1.GET("/recipes", handlers.ListRecipes(db))
		v1.POST("/recipes", handlers.CreateRecipe(db))
		v1.PATCH("/recipes/:uuid", handlers.UpdateRecipe(db))
		v1.DELETE("/recipes/:uuid", handlers.DeleteRecipe(db))
		v1.GET("/recipes/:uuid/cost", handlers.RecipeCost(db))

		// Promotions
		v1.GET("/promotions", handlers.ListPromotions(db))
		v1.POST("/promotions", handlers.CreatePromotion(db))
		v1.PATCH("/promotions/:uuid", handlers.UpdatePromotion(db))
		v1.DELETE("/promotions/:uuid", handlers.DeletePromotion(db))
		v1.GET("/promotions/suggestions", handlers.PromotionSuggestions(db))
		v1.POST("/promotions/apply-to-pos", handlers.ApplyPromoToPOS(db))

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
		admin.GET("/analytics/overview", handlers.AdminOverview(db))
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
	}

	log.Printf("VendIA backend running on :%s (env=%s)", cfg.Port, cfg.Env)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
