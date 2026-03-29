# ══════════════════════════════════════════════════════════════════════════════
# VendIA Backend — Multi-stage Dockerfile
# Stage 1: Compila el binario Go estático
# Stage 2: Imagen final ~15MB basada en alpine (shell + wget para healthcheck)
# ══════════════════════════════════════════════════════════════════════════════

# ── Stage 1: Builder ──────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

# CA certs (para conexiones TLS a Neon.tech) + wget (para healthcheck)
RUN apk --no-cache add ca-certificates wget

WORKDIR /app

# Cachear dependencias antes de copiar el código fuente
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Binario estático: sin libc, compatible con scratch y Cloud Run
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" -o vendia-server ./cmd/server/main.go

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM alpine:3.21

# wget para el HEALTHCHECK del docker-compose (ca-certificates ya incluido en alpine)
RUN apk --no-cache add wget

# El binario de la aplicación
COPY --from=builder /app/vendia-server /vendia-server

EXPOSE 8080

ENTRYPOINT ["/vendia-server"]
