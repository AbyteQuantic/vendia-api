package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port               string
	DatabaseURL        string
	JWTSecret          string
	AllowedOrigins     []string
	Env                string
	RateLimitLogin     int
	GeminiAPIKey       string
	GeminiModel        string
	GeminiImageModel   string
	R2AccountID        string
	R2AccessKeyID      string
	R2SecretKey        string
	R2PublicURL        string
	RedisURL           string
	SupabaseURL        string
	SupabaseServiceKey string

	// Super-admin seed — optional. When both are set, the server
	// upserts a row in admin_users on boot so the /admin/login
	// endpoint has a credential to match against. Missing values
	// skip the seed silently; rotation is explicitly NOT handled
	// here (see internal/database/bootstrap.go).
	SeedAdminEmail    string
	SeedAdminPassword string
	SeedAdminName     string

	// FinOps: assumed list price for PRO (USD / month) — used for
	// "margin at risk" when AI cost / seat approaches 50% of ARPU.
	ProMonthlyPriceUSD float64

	// ePayco payment gateway credentials (Feature 008). All four
	// secrets are read from env vars — never hardcoded (Art. VI / D5).
	// They stay empty in dev/test: the EpaycoService is nil-safe and
	// the unit tests mock the gateway, so `go test` needs no real
	// credentials. EpaycoTestMode toggles the sandbox vs production
	// checkout flag ePayco exposes to its widget.
	EpaycoPublicKey  string
	EpaycoPrivateKey string
	EpaycoPCustID    string
	EpaycoPKey       string
	EpaycoTestMode   bool
}

func Load() *Config {
	_ = godotenv.Load()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	secret := os.Getenv("JWT_SECRET")
	if secret == "" || len(secret) < 32 {
		log.Fatal("JWT_SECRET must be set and at least 32 characters long")
	}

	env := os.Getenv("ENV")
	if env == "" {
		env = "development"
	}

	origins := os.Getenv("ALLOWED_ORIGINS")
	var allowedOrigins []string
	if origins != "" {
		for _, o := range strings.Split(origins, ",") {
			if trimmed := strings.TrimSpace(o); trimmed != "" {
				allowedOrigins = append(allowedOrigins, trimmed)
			}
		}
	}
	// Always include known frontends. The custom domain trio
	// (vendia.store / www.vendia.store / tienda.vendia.store) is
	// hardcoded so a forgotten ALLOWED_ORIGINS env var on Render
	// can't lock the merchant out of their own production app
	// during a deploy. The legacy *.vercel.app / *.onrender.com
	// hosts stay in the allowlist while the DNS migration is in
	// flight — they can be removed in a later cleanup once
	// Namecheap fully propagates.
	knownOrigins := []string{
		// Custom domain (production)
		"https://vendia.store",
		"https://www.vendia.store",
		"https://tienda.vendia.store",
		// Legacy hosts (kept until DNS migration is fully validated)
		"https://vendia-admin.vercel.app",
		"https://vendia-admin.onrender.com",
		// Local dev
		"http://localhost:3000",
		"http://localhost:3099",
	}
	for _, ko := range knownOrigins {
		found := false
		for _, ao := range allowedOrigins {
			if ao == ko {
				found = true
				break
			}
		}
		if !found {
			allowedOrigins = append(allowedOrigins, ko)
		}
	}

	rateLimit := 5
	if v := os.Getenv("RATE_LIMIT_LOGIN"); v != "" {
		if n := parsePositiveInt(v); n > 0 {
			rateLimit = n
		}
	}

	return &Config{
		Port:               port,
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		JWTSecret:          secret,
		AllowedOrigins:     allowedOrigins,
		Env:                env,
		RateLimitLogin:     rateLimit,
		GeminiAPIKey:       os.Getenv("GEMINI_API_KEY"),
		GeminiModel:        os.Getenv("GEMINI_MODEL"),
		GeminiImageModel:   os.Getenv("GEMINI_IMAGE_MODEL"),
		R2AccountID:        os.Getenv("R2_ACCOUNT_ID"),
		R2AccessKeyID:      os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretKey:        os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2PublicURL:        os.Getenv("R2_PUBLIC_URL"),
		RedisURL:           os.Getenv("REDIS_URL"),
		SupabaseURL:        os.Getenv("SUPABASE_URL"),
		SupabaseServiceKey: os.Getenv("SUPABASE_SERVICE_KEY"),

		SeedAdminEmail:     os.Getenv("SEED_ADMIN_EMAIL"),
		SeedAdminPassword:  os.Getenv("SEED_ADMIN_PASSWORD"),
		SeedAdminName:      os.Getenv("SEED_ADMIN_NAME"),
		ProMonthlyPriceUSD: proMonthlyOrDefault(),

		EpaycoPublicKey:  os.Getenv("EPAYCO_PUBLIC_KEY"),
		EpaycoPrivateKey: os.Getenv("EPAYCO_PRIVATE_KEY"),
		EpaycoPCustID:    os.Getenv("EPAYCO_P_CUST_ID"),
		EpaycoPKey:       os.Getenv("EPAYCO_P_KEY"),
		EpaycoTestMode:   parseBoolEnv("EPAYCO_TEST_MODE", true),
	}
}

// parseBoolEnv reads a boolean env var. Accepts "true"/"false"/"1"/"0"
// (case-insensitive). Returns def when unset or unparseable so a typo
// can't silently flip the ePayco sandbox flag. Default true keeps a
// misconfigured deploy in sandbox rather than charging real cards.
func parseBoolEnv(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return def
}

// proMonthlyOrDefault reads PRO_MONTHLY_PRICE_USD; falls back to 29.99.
func proMonthlyOrDefault() float64 {
	if v := strings.TrimSpace(os.Getenv("PRO_MONTHLY_PRICE_USD")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return 29.99
}

func parsePositiveInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
