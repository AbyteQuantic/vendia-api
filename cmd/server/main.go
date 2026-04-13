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
	r.POST("/api/v1/tenant/register", handlers.TenantRegister(db, cfg.JWTSecret))
	r.POST("/api/v1/auth/refresh", handlers.RefreshToken(db, cfg.JWTSecret))

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

	// ── Protected routes (JWT) ───────────────────────────────────────────────
	v1 := r.Group("/api/v1")
	v1.Use(globalLimiter)
	v1.Use(middleware.Auth(cfg.JWTSecret))
	{
		v1.POST("/auth/logout", handlers.Logout(db))

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
		v1.POST("/fiar/remind/:customer_uuid", handlers.RemindCredit(db))

		// Tables (Modo Bar)
		v1.GET("/tables", handlers.ListTables(db))
		v1.POST("/tables", handlers.CreateTable(db))
		v1.PATCH("/tables/:id", handlers.UpdateTable(db))

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

		// Store config
		v1.GET("/store/config", handlers.GetStoreConfig(db))
		v1.PATCH("/store/config", handlers.UpdateStoreConfig(db))

		// Business profile
		v1.GET("/store/profile", handlers.GetBusinessProfile(db))
		v1.PATCH("/store/profile", handlers.UpdateBusinessProfile(db))

		// Payment info (Nequi/Daviplata)
		v1.GET("/tenant/payment-info", handlers.GetPaymentInfo(db))
		v1.PATCH("/tenant/payment-info", handlers.UpdatePaymentInfo(db))
		v1.GET("/payments/qr", handlers.GeneratePaymentQR(db))

		// Logo IA
		v1.POST("/tenant/generate-logo", handlers.GenerateLogo(db, geminiSvc, storageSvc))
		v1.POST("/tenant/upload-logo", handlers.UploadLogo(db, storageSvc))

		// Analytics / Reportes
		v1.GET("/analytics/dashboard", handlers.AnalyticsDashboard(db))
		v1.GET("/analytics/top-products", handlers.TopProducts(db))
		v1.GET("/analytics/photo-coverage", handlers.PhotoCoverage(db))
		v1.GET("/analytics/sales-by-employee", handlers.SalesByEmployee(db))
		v1.GET("/analytics/inventory-health", handlers.InventoryHealth(db))
		v1.GET("/analytics/ingestion-method", handlers.IngestionMethod(db))

		// Rockola (admin)
		v1.GET("/rockola/pending", handlers.PendingSongs(db))
		v1.PATCH("/rockola/:uuid/played", handlers.MarkSongPlayed(db))
		v1.GET("/rockola/search", handlers.SearchSongs(itunesSvc))

		// OCR (legacy endpoint)
		v1.POST("/ocr/invoice", handlers.OCRInvoice(cfg.GeminiAPIKey))
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
	}

	log.Printf("VendIA backend running on :%s (env=%s)", cfg.Port, cfg.Env)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
