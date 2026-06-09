# NEW PROJECT GUIDE

> How to spin up a new project on the Citual platform in under 10 minutes.
> Covers: citual-base, NILEX, online exam platform, online learning platform.

---

## STEP 1: CLONE THE TEMPLATE

```bash
# Option A: GitHub template
gh repo create my-project --template ranakdinesh/citual-base --private
cd my-project

# Option B: Manual copy
cp -r citual-base my-project
cd my-project
git init && git add . && git commit -m "chore: init from citual-base"
```

---

## STEP 2: CONFIGURE

```bash
# Copy env template
cp deployments/.env.example .env

# Edit .env — minimum required:
# DATABASE_URL=postgres://citual:citual@localhost:5432/my-project
# IDENTITY_JWT_SECRET=<generate: openssl rand -hex 32>
# IDENTITY_ISSUER=https://auth.myproject.com  (or http://localhost:8090 for dev)

# Create keys directory
mkdir -p keys && chmod 700 keys
# RSA key is auto-generated on first run at IDENTITY_RSA_KEY_PATH
```

---

## STEP 3: START

```bash
docker-compose up -d

# Verify
curl http://localhost:8090/health
# {"status":"ok","modules":["identity"]}
```

---

## STEP 4: BRAND THE UI

```bash
# Set theme
echo "NEXT_PUBLIC_THEME=myproject" >> ui/auth/.env.local

# Create theme file
cat > ui/shared/theme/myproject.ts << 'EOF'
import { CitualTheme } from './types'
export const myprojectTheme: CitualTheme = {
  name: 'myproject',
  logo: { src: '/logo.svg', width: 120, height: 32, alt: 'My Project' },
  fonts: { sans: 'var(--font-inter)' },
  colors: {
    // Change accent color to your brand
    accent:       '213 94% 58%',   // your brand blue
    accentHover:  '213 94% 50%',
    // ... rest of tokens
  },
  // ... radius, auth
}
EOF

# Add logo
cp my-logo.svg ui/auth/public/logo.svg

# Start UI
cd ui/auth && npm run dev
```

---

## STEP 5: ADD APPLICATION MODULES

Skip this step for auth-only deployments (citual-base).

### For NILEX

```bash
# Create module structure
mkdir -p modules/workforce/core/{domain,ports,services}
mkdir -p modules/workforce/adapters/{postgres/sqlc,http/handlers,grpc}
mkdir -p modules/workforce/sql/{migrations,queries}

# Create module scaffold
cat > modules/workforce/module.go << 'EOF'
package workforce

import (
    "context"
    "fmt"
    "github.com/ranakdinesh/citual/platform"
    "github.com/ranakdinesh/citual/modules/workforce/sql/migrations"
)

type Module struct{}

func New() platform.Module { return &Module{} }

func (m *Module) Definition() platform.ModuleDefinition {
    return platform.ModuleDefinition{
        Name:       "workforce",
        Version:    "1.0.0",
        HTTPPrefix: "/workforce",
        Permissions: []string{
            "workforce.jobs.create",
            "workforce.jobs.publish",
            "workforce.jobs.delete",
            "workforce.workers.view",
            "workforce.workers.approve",
            "workforce.matching.run",
            "workforce.reports.export",
        },
    }
}

func (m *Module) Mount(infra *platform.Infra) error {
    if err := infra.Migrations.Run(context.Background(), "workforce", migrations.FS); err != nil {
        return fmt.Errorf("workforce migrations: %w", err)
    }
    // Wire repos, services, routes here
    return nil
}

func (m *Module) HealthCheck(ctx context.Context) error { return nil }
EOF

# Register in cmd/main.go
# Add: workforce.New() to platform.Register call

# Add sqlc block to sqlc.yaml for workforce module

# Generate sqlc
sqlc generate

# Build
go build ./...
```

### For Online Exam Platform (examly)

```bash
# Create all exam modules
for module in reading writing listening speaking cat proctoring results; do
    mkdir -p modules/$module/core/{domain,ports,services}
    mkdir -p modules/$module/adapters/{postgres/sqlc,http/handlers}
    mkdir -p modules/$module/sql/{migrations,queries}
done

# Register all in cmd/main.go:
# reading.New(), writing.New(), listening.New(), speaking.New(),
# cat.New(), proctoring.New(), results.New()

# Theme
cp examly-logo.svg ui/auth/public/logo.svg
# Set NEXT_PUBLIC_THEME=examly in ui/auth/.env.local
```

---

## DOCKER COMPOSE

```yaml
# deployments/docker-compose.yml

version: '3.9'

services:
  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB:       ${POSTGRES_DB:-citual}
      POSTGRES_USER:     ${POSTGRES_USER:-citual}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-citual}
    volumes:
      - pg_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER:-citual}"]
      interval: 5s
      timeout: 5s
      retries: 10
    ports:
      - "5432:5432"   # remove in production

  redis:
    image: redis:7-alpine
    restart: unless-stopped
    volumes:
      - redis_data:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      retries: 10

  app:
    build:
      context: ..
      dockerfile: deployments/Dockerfile
    restart: unless-stopped
    env_file: .env
    environment:
      DATABASE_URL: postgres://${POSTGRES_USER:-citual}:${POSTGRES_PASSWORD:-citual}@postgres:5432/${POSTGRES_DB:-citual}?sslmode=disable
      REDIS_URL:    redis://redis:6379
    ports:
      - "8090:8090"
      - "9090:9090"
    volumes:
      - ./keys:/app/keys     # RSA key persistence
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy

  ui:
    build:
      context: ../ui/auth
      dockerfile: Dockerfile
    restart: unless-stopped
    environment:
      NEXT_PUBLIC_CITUAL_URL: http://app:8090
      NEXT_PUBLIC_THEME: ${UI_THEME:-citual}
    ports:
      - "3000:3000"
    depends_on:
      - app

volumes:
  pg_data:
  redis_data:
```

---

## .env.example

```bash
# ─── Postgres ────────────────────────────────────────────
POSTGRES_DB=citual
POSTGRES_USER=citual
POSTGRES_PASSWORD=changeme

# ─── Platform ────────────────────────────────────────────
ENV=development
LOG_LEVEL=info
HTTP_PORT=8090
GRPC_PORT=9090

# ─── Database ────────────────────────────────────────────
# Overridden by docker-compose for container deployments
DATABASE_URL=postgres://citual:changeme@localhost:5432/citual?sslmode=disable
DB_MAX_CONNS=40
DB_MIN_CONNS=5

# ─── Redis ───────────────────────────────────────────────
REDIS_URL=redis://localhost:6379

# ─── Identity Module ─────────────────────────────────────
# REQUIRED: generate with: openssl rand -hex 32
IDENTITY_JWT_SECRET=

# Key file path — auto-generated on first run
IDENTITY_RSA_KEY_PATH=./keys/private.pem

# Change to your actual domain before going live
IDENTITY_ISSUER=http://localhost:8090

IDENTITY_ACCESS_TTL=24h
IDENTITY_REFRESH_TTL=720h
IDENTITY_OTP_EXPIRY=2m
IDENTITY_OTP_MAX_ATTEMPTS=3

# ─── Communication ───────────────────────────────────────
# Provider: stub | resend | twilio
IDENTITY_COMM_PROVIDER=stub

# Resend (email)
# RESEND_API_KEY=
# RESEND_FROM=noreply@yourdomain.com

# Twilio (SMS)
# TWILIO_ACCOUNT_SID=
# TWILIO_AUTH_TOKEN=
# TWILIO_FROM_NUMBER=

# ─── UI ──────────────────────────────────────────────────
UI_THEME=citual
```

---

## DOCKERFILE

```dockerfile
# deployments/Dockerfile

FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server ./cmd/main.go

# ─── Runtime ─────────────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/server .

RUN mkdir -p /app/keys && chmod 700 /app/keys

EXPOSE 8090 9090

CMD ["/app/server"]
```

---

## HEALTH CHECK ENDPOINT

```
GET /health

Response 200:
{
  "status": "ok",
  "version": "1.0.0",
  "modules": ["identity", "workforce"],
  "db": "ok",
  "redis": "ok"
}

Response 503 (if any dependency is down):
{
  "status": "degraded",
  "modules": ["identity"],
  "db": "ok",
  "redis": "error: connection refused"
}
```

---

## PRODUCTION CHECKLIST

Before going live on any project:

```
Infrastructure
[ ] IDENTITY_JWT_SECRET is a real random secret (openssl rand -hex 32)
[ ] IDENTITY_ISSUER is set to actual domain
[ ] keys/ directory has correct permissions (chmod 700)
[ ] DATABASE_URL uses SSL in production (sslmode=require)
[ ] REDIS_URL is a dedicated Redis instance (not shared)
[ ] Caddy or nginx configured for TLS termination
[ ] docker-compose does not expose postgres port 5432 publicly

Security
[ ] All Priority 1 identity fixes applied
[ ] Rate limiting active on auth endpoints
[ ] OTP hashing verified (not plaintext)
[ ] Token revocation working (Redis)

Operations
[ ] PostgreSQL backups configured (daily minimum)
[ ] Log aggregation set up (Loki / CloudWatch / etc.)
[ ] Health check endpoint monitored
[ ] Alerting on 5xx error rate
```
