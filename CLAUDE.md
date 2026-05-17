# VendIA Backend - Claude Code Context

> Parte del workspace **VendIA**. Antes de tocar código lee, en orden:
> [`../CONSTITUTION.md`](../CONSTITUTION.md) → [`../AGENTS.md`](../AGENTS.md) → este archivo.

## Desarrollo guiado por especificación (Spec-Driven)

Ningún cambio de comportamiento, contrato de API o esquema entra sin un `spec`
en `../specs/`. Flujo: **specify → clarify → plan → tasks → implement →
analyze** (slash commands en `../.claude/commands/`).

Adaptación a este repo (Go):
- **RED:** prueba `testify` que falla por la razón correcta — `*_test.go` junto al código.
- **GREEN:** implementación mínima; handlers `func(db *gorm.DB) gin.HandlerFunc`.
- **REFACTOR:** extrae lógica a `services/`; archivos < 800 líneas.
- **Verificación:** `go build ./...` + `go vet ./...` + `go test ./... -race`, cobertura ≥ 80%.
- **Trazabilidad:** primera línea de cada archivo nuevo → `// Spec: specs/NNN-slug/spec.md`.
- **Migraciones:** Constitución Art. X — solo AutoMigrate, aditivo y retrocompatible;
  UUID nullable como `*string`; backfills en el bootstrap de Go, no en `.sql` sueltos.

## Project Overview
VendIA is an offline-first POS (Point of Sale) system for small businesses in Colombia (tiendas de barrio, minimercados, bares, restaurantes). Target audience: adults 50+ running informal businesses. SaaS model with monthly subscription. "Zero Cognitive Friction" is the golden rule.

## Tech Stack
- **Language:** Go 1.25+ with Gin web framework (NOT Fiber)
- **Database:** PostgreSQL with GORM ORM (auto-migrate, no raw SQL in handlers)
- **Auth:** JWT HS256 (15min access + 30day refresh tokens via `golang-jwt/jwt/v5`)
- **AI:** Google Gemini 2.0 Flash (invoice OCR, photo enhance, logo generation)
- **Storage:** Cloudflare R2 via AWS SDK Go v2 (images converted to WebP, max 50KB)
- **Cache:** Redis (planned, env var ready)
- **APIs:** Open Food Facts (free, barcode lookup), iTunes Search (free, rockola)
- **Messaging:** WhatsApp deep links (`wa.me/` URLs via url_launcher, no official API)
- **Testing:** testify (assert/require), race detector enabled
- **Migrations:** GORM AutoMigrate + goose (SQL migrations in `/migrations/`)

## Architecture Patterns
- **Multi-tenant:** ALL queries filtered by `tenant_id` from JWT claims via `middleware.GetTenantID(c)`
- **UUID v4:** Primary keys generated client-side for offline support (`BaseModel.BeforeCreate` fallback)
- **Idempotent:** Creation endpoints accept client-provided UUID — duplicate UUID = no duplicate
- **Offline-first:** Sync via `POST /api/v1/sync/batch` with Last-Write-Wins (LWW) conflict resolution
- **Nil-safe services:** External services (Gemini, R2, OFF, iTunes) initialized as pointers, handlers check nil before use
- **Spanish UX:** All error messages in Spanish for Colombian end users

## Project Structure
```
cmd/server/main.go              → Entry point, service init, route registration
internal/
  auth/jwt.go                   → JWT generation (access+refresh+admin) and validation
  config/config.go              → Env config loader (.env via godotenv)
  database/database.go          → DB connection (5x retry) + GORM AutoMigrate
  handlers/                     → HTTP handlers (one file per module)
    employees.go                → CRUD + PIN verify
    inventory.go                → Scan invoice (Gemini), barcode lookup (OFF), photo upload/enhance
    orders.go                   → KDS order tickets, status transitions, close→sale
    suppliers.go                → CRUD + WhatsApp order message
    recipes.go                  → CRUD + cost calculation with live ingredient prices
    promotions.go               → CRUD + AI suggestions (expiring/low rotation)
    store.go                    → Store config, public catalog, web orders
    whatsapp_handler.go         → Receipt, credit reminder, payment QR
    analytics_tenant.go         → Dashboard, top products, photo coverage, employee sales, inventory health
    logo.go                     → Generate (Gemini) + upload logo to R2
    rockola.go                  → Song suggestions (public), pending queue, iTunes search
    receipts.go                 → Sales history, receipt detail, reprint
    account_ws.go               → Public account view + phone verification
    products.go                 → Product CRUD + seed
    sales.go                    → POS sale creation + today stats + list
    customers.go                → Customer CRUD
    credits.go                  → Credit accounts + payments
    tables.go                   → Bar mode table management
    tabs.go                     → Open tab (legacy bar accounts)
    login.go                    → Phone+password auth
    tenant_register.go          → Onboarding (tenant+owner employee+optional employees)
    auth.go                     → Token refresh + logout
    ocr.go                      → Legacy OCR endpoint
    pagination.go               → Shared pagination utilities
    health.go                   → /ping
    admin_analytics.go          → Super admin system-wide analytics
    payment_info.go             → Nequi/Daviplata config
    sync.go                     → Offline sync batch
  middleware/
    auth_middleware.go           → JWT extraction → tenant_id + claims in context
    super_admin.go               → IsSuperAdmin check
    rate_limiter.go              → Per-IP sliding window (5/min login, 100/min global)
    logger.go                    → Request logging with tenant_id
    dev_only.go                  → Block endpoints in production
  models/                        → GORM models with BaseModel (UUID + timestamps + soft delete)
    base.go                      → BaseModel{ID, CreatedAt, UpdatedAt, DeletedAt} + IsValidUUID
    tenant.go                    → Business account + store/delivery fields
    employee.go                  → Staff with PIN + role (admin|cashier)
    product.go                   → Products with purchase_price, expiry, ingestion_method
    sale.go                      → Sales with employee tracking + receipt number
    customer.go                  → Customer profiles
    credit_account.go            → El Fiar (buy now pay later)
    credit_payment.go            → Credit payments
    supplier.go                  → Supplier contacts
    order_ticket.go              → KDS orders with status machine + items
    recipe.go                    → Recipes with ingredients
    promotion.go                 → Price promotions
    rockola.go                   → Song suggestions
    open_tab.go                  → Legacy bar tabs (JSONB items)
    refresh_token.go             → JWT refresh tokens
    admin_user.go                → Super admin users
  services/
    gemini_service.go            → Invoice scan, photo enhance, logo generation
    r2_service.go                → Cloudflare R2 upload/download via S3 API
    openfoodfacts_service.go     → Barcode→product lookup
    whatsapp_service.go          → Message templates + wa.me URL builder
    itunes_service.go            → Music search for rockola
    pricing_service.go           → Colombian price rounding ($50 COP) + margin calc
    ocr_service.go               → Legacy regex+Gemini OCR
    credit_service.go            → Credit payment registration
    sync_service.go              → Offline sync engine (LWW)
migrations/                      → SQL migrations (goose format)
```

## Key Conventions
- Handler signature: `func HandlerName(db *gorm.DB) gin.HandlerFunc` or with service deps
- Tenant isolation: `middleware.GetTenantID(c)` in EVERY handler query
- Pagination: `parsePagination(c)` → `newPaginatedResponse(data, total, p)` (max 100/page)
- Request structs: defined inline inside handler functions
- PATCH endpoints: pointer fields (`*string`, `*float64`) for partial updates
- Error responses: `gin.H{"error": "mensaje en español"}`
- Success responses: `gin.H{"data": result}` or `gin.H{"message": "..."}`
- IDs: all UUID v4 strings, client can send `"id"` field for offline-first creation
- Soft deletes: `gorm.DeletedAt` on all models via BaseModel

## Business Rules
- **Roles:** admin (full access) and cashier (sell + credit only)
- **Charge modes:** pre_payment (tiendas, cobro inmediato) vs post_payment (bares, cuentas abiertas)
- **Business types:** tienda_barrio | minimercado | bar | miscelanea
- **Bar mode:** enables tables, KDS order tickets, meseros
- **Employee PIN:** 4 digits, bcrypt hashed, for shift start verification
- **Pricing:** always round to nearest $50 COP via `math.Ceil(amount/50) * 50`
- **Order status machine:** nuevo → preparando → listo → cobrado (or cancelado from nuevo/preparando)
- **Closing an order** automatically creates a Sale record

## Environment Variables
```
DATABASE_URL          → PostgreSQL connection string (required)
JWT_SECRET            → Min 32 chars, HS256 signing key (required)
PORT                  → Default 8080
ENV                   → development | production
ALLOWED_ORIGINS       → Comma-separated CORS origins
RATE_LIMIT_LOGIN      → Default 5 per minute
GEMINI_API_KEY        → Google Gemini API key (optional, enables AI features)
GEMINI_MODEL          → Default gemini-2.0-flash
R2_ACCOUNT_ID         → Cloudflare account ID (optional, enables R2 storage)
R2_ACCESS_KEY_ID      → R2 access key
R2_SECRET_ACCESS_KEY  → R2 secret key
R2_PUBLIC_URL         → R2 public base URL (e.g., https://r2.vendia.co)
REDIS_URL             → Redis connection (optional, planned for cache)
```

## API Endpoints Summary (70+)
- **Public (no auth):** /ping, /login, /register, /refresh, store catalog, web orders, rockola suggest, account view
- **Protected (JWT):** employees, products, inventory IA, sales, customers, credits, orders/KDS, suppliers, recipes, promotions, store config, analytics, logo, rockola admin, receipts, sync, payments
- **Admin (super_admin):** system analytics, tenant management, subscription updates

## Testing
- `go test ./... -race` → all pass (85 PASS, 5 SKIP without Docker DB)
- Integration tests (tenant_register) skip gracefully with TCP pre-check when no PostgreSQL
- Unit tests cover: models, services (pricing, WhatsApp, Gemini, OCR), handler validation, middleware, auth, config
- Quality gates: `go build`, `go vet`, `go test -race` all pass clean
