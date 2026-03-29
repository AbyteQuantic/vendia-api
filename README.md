# VendIA API

Backend del POS inteligente para tenderos colombianos.

## Tech Stack

- **Go 1.25+** con Gin
- **PostgreSQL** con GORM
- **JWT** HS256 (access + refresh tokens)
- **Gemini 2.0 Flash** (OCR facturas, fotos, logos)
- **Cloudflare R2** (almacenamiento de imágenes)

## Setup Local

```bash
cp .env.example .env   # Configurar variables
go mod download
go run ./cmd/server/main.go
```

## Variables de Entorno

| Variable | Descripción | Requerido |
|---|---|---|
| `DATABASE_URL` | PostgreSQL connection string | Si |
| `JWT_SECRET` | Min 32 chars, HS256 | Si |
| `PORT` | Default 8080 | No |
| `ENV` | development / production | No |
| `GEMINI_API_KEY` | Google Gemini API key | No |
| `ALLOWED_ORIGINS` | CORS origins (comma-separated) | No |

## API

70+ endpoints organizados en 18 módulos. Health check en `GET /ping`.

## Tests

```bash
go test ./... -race
```
