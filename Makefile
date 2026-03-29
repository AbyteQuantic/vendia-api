.PHONY: local build logs ps down clean test lint

COMPOSE = docker compose -f ../docker-compose.yml

# ── Local stack (backend + DB in Docker) ──────────────────────────────────────
local:
	$(COMPOSE) up -d --build
	@echo ""
	@echo "✅ Stack local running:"
	@echo "   Backend  → http://localhost:8080/ping"
	@echo "   Postgres → localhost:5432 (vendia / vendia_secret)"

# ── Rebuild without cache ─────────────────────────────────────────────────────
build:
	$(COMPOSE) build --no-cache

# ── Logs ──────────────────────────────────────────────────────────────────────
logs:
	$(COMPOSE) logs -f

logs-backend:
	$(COMPOSE) logs -f backend

logs-db:
	$(COMPOSE) logs -f postgres

# ── Status ────────────────────────────────────────────────────────────────────
ps:
	$(COMPOSE) ps

# ── Stop containers (preserves data) ─────────────────────────────────────────
down:
	$(COMPOSE) down

# ── Destroy everything including volumes ──────────────────────────────────────
clean:
	$(COMPOSE) down -v --remove-orphans
	@echo "⚠️  Volumes deleted. DB is empty."

# ── Tests ─────────────────────────────────────────────────────────────────────
test:
	go test ./... -cover -race

lint:
	go vet ./...
