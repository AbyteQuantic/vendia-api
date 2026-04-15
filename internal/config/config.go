package config

import (
	"log"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Port           string
	DatabaseURL    string
	JWTSecret      string
	AllowedOrigins []string
	Env            string
	RateLimitLogin int
	GeminiAPIKey      string
	GeminiModel       string
	GeminiImageModel  string
	R2AccountID    string
	R2AccessKeyID  string
	R2SecretKey    string
	R2PublicURL       string
	RedisURL          string
	SupabaseURL       string
	SupabaseServiceKey string
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
	// Always include known frontends
	knownOrigins := []string{
		"https://vendia-admin.vercel.app",
		"https://vendia-admin.onrender.com",
		"http://localhost:3000",
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
		Port:           port,
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		JWTSecret:      secret,
		AllowedOrigins: allowedOrigins,
		Env:            env,
		RateLimitLogin: rateLimit,
		GeminiAPIKey:      os.Getenv("GEMINI_API_KEY"),
		GeminiModel:       os.Getenv("GEMINI_MODEL"),
		GeminiImageModel:  os.Getenv("GEMINI_IMAGE_MODEL"),
		R2AccountID:    os.Getenv("R2_ACCOUNT_ID"),
		R2AccessKeyID:  os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretKey:    os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2PublicURL:       os.Getenv("R2_PUBLIC_URL"),
		RedisURL:          os.Getenv("REDIS_URL"),
		SupabaseURL:       os.Getenv("SUPABASE_URL"),
		SupabaseServiceKey: os.Getenv("SUPABASE_SERVICE_KEY"),
	}
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
