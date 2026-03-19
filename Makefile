build:
	go build -o bin/agentclawd ./cmd/agentclawd

run:
	go run ./cmd/agentclawd

deps:
	go mod tidy
	go get github.com/gorilla/websocket@v1.5.1
	go get github.com/mattn/go-sqlite3@v1.14.22
	go get github.com/robfig/cron/v3@v3.0.1
	go get github.com/spf13/viper@v1.18.2
	go get github.com/rs/zerolog@v1.32.0
	go get github.com/google/uuid@v1.6.0

# ─── Docker ───────────────────────────────────────────────────────────────────
# First time setup
docker-init:
	cp .env.example .env
	mkdir -p data memory/agents state/scope state/old state/resolved static
	cp docs/project.template.md memory/project.md
	@echo "✅ Edit .env with your API keys then run: make docker-up"

# Start all services
docker-up:
	docker compose up -d
	@echo "✅ AgentClaw running at http://localhost:8080"

# Stop all services
docker-down:
	docker compose down

# Rebuild image and restart (after code changes)
docker-rebuild:
	docker compose down
	docker compose build --no-cache
	docker compose up -d

# Tail logs
docker-logs:
	docker compose logs -f agentclaw

# Open shell inside container (for debugging)
docker-shell:
	docker compose exec agentclaw sh

# Remove everything including volumes
docker-clean:
	docker compose down -v
	docker rmi agentclaw-agentclaw 2>/dev/null || true

# ─── Cross-compile ────────────────────────────────────────────────────────────
build-linux:
	GOOS=linux GOARCH=amd64 go build -o bin/agentclawd-linux ./cmd/agentclawd

build-arm64:
	GOOS=linux GOARCH=arm64 go build -o bin/agentclawd-arm64 ./cmd/agentclawd

# ─── Local setup (no Docker) ──────────────────────────────────────────────────
init:
	mkdir -p memory bin
	cp docs/project.template.md memory/project.md
	@echo "Edit memory/project.md then run: make run"

.PHONY: build run deps docker-init docker-up docker-down docker-rebuild \
        docker-logs docker-shell docker-clean build-linux build-arm64 init

