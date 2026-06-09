.PHONY: dev build test lint sqlc keys docker-up docker-down tidy

# ── Local dev ──────────────────────────────────────────────────────────────
dev:
	go run ./cmd/main.go

build:
	go build -o bin/spur ./cmd/main.go

test:
	go test ./... -race -count=1

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

# ── Keys ───────────────────────────────────────────────────────────────────
keys:
	@mkdir -p keys && chmod 700 keys
	@if [ ! -f keys/private.pem ]; then \
		openssl genrsa -out keys/private.pem 2048 && chmod 600 keys/private.pem; \
		echo "RSA key generated at keys/private.pem"; \
	else \
		echo "keys/private.pem already exists"; \
	fi

# ── sqlc ───────────────────────────────────────────────────────────────────
sqlc:
	sqlc generate

# ── Docker ─────────────────────────────────────────────────────────────────
docker-up:
	docker compose -f deployments/docker-compose.yml up --build -d

docker-down:
	docker compose -f deployments/docker-compose.yml down

docker-logs:
	docker compose -f deployments/docker-compose.yml logs -f backend

# ── Setup (first time) ─────────────────────────────────────────────────────
setup: keys
	@cp -n deployments/.env.example deployments/.env || true
	@echo ""
	@echo "Next steps:"
	@echo "  1. Edit deployments/.env and fill in FOSITE_GLOBAL_SECRET and AUTH_CLIENT_ID"
	@echo "  2. Run: make docker-up"
	@echo "  3. Open: http://localhost:3000"
