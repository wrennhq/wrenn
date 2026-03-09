# CLAUDE.md — Wrenn Sandbox

## Project Overview

Wrenn Sandbox is a microVM-based code execution platform. Users create isolated sandboxes (Firecracker microVMs), run code inside them, and get output back via SDKs (Python, TypeScript, Go). Think E2B but with persistent sandboxes, pool-based pricing, and a single-binary deployment story.

## Architecture

```
User SDK (Python/TS/Go)
    │
    │ HTTPS / WebSocket
    ▼
Control Plane (Go binary, single process)
    ├── REST API (chi router)
    ├── Admin UI (htmx + Go templates)
    ├── Scheduler (picks host for new sandboxes)
    ├── State DB (PostgreSQL via pgx + goose migrations)
    ├── Lifecycle Manager (background goroutine)
    └── gRPC client → Host Agent
    │
    │ gRPC (mTLS)
    ▼
Host Agent (Go binary, one per physical machine)
    ├── VM Manager (Firecracker HTTP API via Unix socket)
    ├── Network Manager (TAP devices, NAT, IP allocator)
    ├── Filesystem Manager (CoW rootfs clones)
    ├── Envd Client (HTTP/Connect RPC to guest agent via TAP network)
    ├── Snapshot Manager (pause/hibernate/resume)
    ├── Metrics Exporter (Prometheus)
    └── gRPC server (listens for control plane)
    │
    │ HTTP over TAP network (veth + namespace isolation)
    ▼
envd (Go binary, runs inside each microVM via wrenn-init)
    ├── ProcessService (exec commands, stream stdout/stderr)
    ├── FilesystemService (read/write/list files)
    └── Terminal (PTY handling for interactive sessions)
```

## Key Decisions

- **Language**: Everything is Go. No Python, no Node.js, no separate frontend.
- **Guest agent**: envd is extracted from E2B's open-source repo (e2b-dev/infra, Apache 2.0). The orchestrator VM management code is also adapted from E2B.
- **Database**: PostgreSQL. Migrations via goose (plain SQL files).
- **Admin UI**: htmx + Go html/template + chi router, served from the control plane binary. No SPA, no React, no build step.
- **API framework**: chi router for HTTP. Standard grpc-go for gRPC.
- **Billing**: Lago (external service, integrated via API). Not part of this codebase — we send usage events to Lago.
- **No separate reverse proxy binary**. Port forwarding is handled within the control plane or host agent directly if needed later.

## Directory Structure

```
wrenn-sandbox/
├── CLAUDE.md                          # This file
├── Makefile                           # Build all binaries, run migrations, generate proto
├── go.mod                             # github.com/wrenn-dev/wrenn-sandbox
├── go.sum
├── .env.example
│
├── cmd/
│   ├── control-plane/
│   │   └── main.go                    # Entry: HTTP server + gRPC client + lifecycle manager
│   └── host-agent/
│       └── main.go                    # Entry: gRPC server + VM management
│
├── envd/                              # Guest agent (extracted from E2B, separate go.mod)
│   ├── go.mod
│   ├── main.go
│   ├── Makefile
│   └── internal/                      # Process exec, filesystem, PTY, PID 1 handling
│
├── proto/
│   ├── envd/                          # From E2B: ProcessService, FilesystemService
│   │   ├── process.proto
│   │   ├── filesystem.proto
│   │   └── gen/                       # Generated Go stubs
│   └── hostagent/                     # Our definition: control plane ↔ host agent
│       ├── hostagent.proto
│       └── gen/
│
├── internal/
│   │
│   │ ── CONTROL PLANE ──
│   ├── api/
│   │   ├── server.go                  # chi router setup, middleware
│   │   ├── middleware.go              # Auth, rate limiting, request logging
│   │   ├── handlers_sandbox.go        # CRUD for sandboxes
│   │   ├── handlers_exec.go           # Execute commands in sandboxes
│   │   ├── handlers_files.go          # Upload/download files
│   │   └── handlers_terminal.go       # WebSocket terminal sessions
│   │
│   ├── admin/                         # Admin UI (htmx + Go templates)
│   │   ├── handlers.go                # Page handlers (dashboard, sandbox detail, etc.)
│   │   ├── templates/
│   │   │   ├── layout.html            # Base layout with htmx, navigation
│   │   │   ├── dashboard.html         # Overview: active sandboxes, resource usage
│   │   │   ├── sandboxes.html         # List all sandboxes with status
│   │   │   ├── sandbox_detail.html    # Single sandbox: logs, metrics, audit trail
│   │   │   └── partials/              # htmx partial templates for dynamic updates
│   │   │       ├── sandbox_row.html
│   │   │       ├── metrics_card.html
│   │   │       └── audit_log.html
│   │   └── static/                    # Minimal CSS (no build step)
│   │       └── style.css
│   │
│   ├── auth/
│   │   ├── apikey.go                  # API key validation
│   │   └── ratelimit.go
│   │
│   ├── scheduler/
│   │   ├── scheduler.go               # Interface definition
│   │   ├── single_host.go             # Default: always picks the one registered host
│   │   └── least_loaded.go            # Multi-host: picks host with most available capacity
│   │
│   ├── lifecycle/
│   │   └── manager.go                 # Background goroutine: auto-pause, auto-hibernate, auto-destroy
│   │
│   │ ── HOST AGENT ──
│   ├── vm/
│   │   ├── manager.go                 # CreateVM, DestroyVM (wraps Firecracker Go SDK)
│   │   ├── config.go                  # Build Firecracker config from sandbox request
│   │   └── jailer.go                  # Jailer configuration for production
│   │
│   ├── network/
│   │   ├── manager.go                 # SetupNetwork, TeardownNetwork (TAP + NAT)
│   │   ├── allocator.go               # IP pool allocator (/30 subnets from 10.0.0.0/16)
│   │   └── nat.go                     # iptables/nftables rule management
│   │
│   ├── filesystem/
│   │   ├── images.go                  # Base image registry
│   │   └── clone.go                   # CoW rootfs clones (cp --reflink)
│   │
│   ├── envdclient/
│   │   ├── client.go                  # gRPC client wrapper for envd
│   │   ├── dialer.go                  # HTTP transport to envd via TAP network
│   │   └── health.go                  # Health check with retry
│   │
│   ├── snapshot/
│   │   ├── manager.go                 # Pause/resume coordination
│   │   ├── local.go                   # Local disk snapshot storage
│   │   └── remote.go                  # S3/GCS upload/download for hibernate
│   │
│   ├── metrics/
│   │   ├── collector.go               # Read cgroup stats per sandbox
│   │   └── exporter.go                # Prometheus /metrics endpoint
│   │
│   │ ── SHARED ──
│   ├── models/
│   │   ├── sandbox.go                 # Sandbox struct, status enum, state machine
│   │   └── host.go                    # Host struct, capacity tracking
│   │
│   ├── id/
│   │   └── id.go                      # Generate sandbox IDs: "sb-" + 8 hex chars
│   │
│   └── config/
│       └── config.go                  # Configuration loading (env vars + YAML)
│
├── db/
│   ├── migrations/                    # goose SQL migrations
│   │   ├── 00001_initial.sql
│   │   ├── 00002_add_persistence.sql
│   │   └── 00003_add_audit_events.sql
│   └── queries/                       # SQL queries (used with sqlc or raw pgx)
│       ├── sandboxes.sql
│       ├── hosts.sql
│       └── audit.sql
│
├── images/                            # Rootfs build scripts
│   ├── build-rootfs.sh
│   ├── docker-to-rootfs.sh
│   └── templates/
│       ├── minimal/build.sh
│       ├── python311/build.sh
│       └── node20/build.sh
|
├── deploy/
│   ├── systemd/
│   │   ├── wrenn-control-plane.service
│   │   └── wrenn-host-agent.service
│   └── ansible/
│       └── playbook.yml
│
├── scripts/
│   ├── setup-host.sh
│   ├── generate-proto.sh
│   └── dev.sh
│
└── tests/
    ├── integration/
    │   ├── sandbox_lifecycle_test.go
    │   ├── networking_test.go
    │   └── snapshot_test.go
    └── load/
        └── concurrent_test.go
```

## Database

### Tech Stack
- PostgreSQL (via pgx/v5 driver, no ORM)
- goose for migrations (plain SQL, up/down)
- sqlc for type-safe query generation (optional, can use raw pgx)

### Migration Convention
```
db/migrations/
├── 00001_initial.sql
├── 00002_add_persistence.sql
└── ...
```

Each migration file uses goose format:
```sql
-- +goose Up
CREATE TABLE sandboxes (...);

-- +goose Down
DROP TABLE sandboxes;
```

Run migrations:
```bash
# Apply all pending migrations
goose -dir db/migrations postgres "$DATABASE_URL" up

# Rollback last migration
goose -dir db/migrations postgres "$DATABASE_URL" down

# Check current status
goose -dir db/migrations postgres "$DATABASE_URL" status

# Create a new migration
goose -dir db/migrations create add_new_table sql
```

### Core Tables

**sandboxes** — Every sandbox created on the platform
```sql
CREATE TABLE sandboxes (
    id               TEXT PRIMARY KEY,
    owner_id         TEXT NOT NULL,
    host_id          TEXT NOT NULL,
    template         TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending',
    vcpus            INTEGER DEFAULT 1,
    memory_mb        INTEGER DEFAULT 512,
    timeout_sec      INTEGER DEFAULT 300,
    guest_ip         TEXT,
    vsock_cid        INTEGER,
    snapshot_path    TEXT,
    created_at       TIMESTAMPTZ DEFAULT NOW(),
    started_at       TIMESTAMPTZ,
    paused_at        TIMESTAMPTZ,
    last_active_at   TIMESTAMPTZ,
    metadata         JSONB DEFAULT '{}'
);

CREATE INDEX idx_sandboxes_owner ON sandboxes(owner_id);
CREATE INDEX idx_sandboxes_status ON sandboxes(status);
CREATE INDEX idx_sandboxes_host ON sandboxes(host_id);
```

**hosts** — Registered host agents
```sql
CREATE TABLE hosts (
    id               TEXT PRIMARY KEY,
    grpc_endpoint    TEXT NOT NULL,
    total_vcpus      INTEGER,
    total_memory_mb  INTEGER,
    used_vcpus       INTEGER DEFAULT 0,
    used_memory_mb   INTEGER DEFAULT 0,
    sandbox_count    INTEGER DEFAULT 0,
    status           TEXT DEFAULT 'healthy',
    last_heartbeat   TIMESTAMPTZ
);
```

**audit_events** — Every exec/file operation
```sql
CREATE TABLE audit_events (
    id               BIGSERIAL PRIMARY KEY,
    sandbox_id       TEXT NOT NULL REFERENCES sandboxes(id),
    owner_id         TEXT NOT NULL,
    event_type       TEXT NOT NULL,
    command          TEXT,
    exit_code        INTEGER,
    duration_ms      INTEGER,
    stdout_bytes     INTEGER,
    stderr_bytes     INTEGER,
    created_at       TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_audit_sandbox ON audit_events(sandbox_id);
CREATE INDEX idx_audit_owner ON audit_events(owner_id);
CREATE INDEX idx_audit_created ON audit_events(created_at);
```

**api_keys** — Authentication
```sql
CREATE TABLE api_keys (
    id               TEXT PRIMARY KEY,
    key_hash         TEXT NOT NULL UNIQUE,
    owner_id         TEXT NOT NULL,
    plan             TEXT DEFAULT 'hobby',
    pool_vcpus       INTEGER DEFAULT 2,
    pool_memory_mb   INTEGER DEFAULT 8192,
    pool_storage_mb  INTEGER DEFAULT 20480,
    is_active        BOOLEAN DEFAULT true,
    created_at       TIMESTAMPTZ DEFAULT NOW()
);
```

### Sandbox State Machine
```
PENDING → STARTING → RUNNING → PAUSED → HIBERNATED
                       │          │
                       ↓          ↓
                    STOPPED    STOPPED
                       │
                       ↓
                    (destroyed/cleaned up)

Also: any state → ERROR (on crash/failure)
PAUSED → RUNNING (resume from warm snapshot)
HIBERNATED → RUNNING (resume from cold snapshot, slower)
```

## Admin UI (htmx)

The control plane serves an admin dashboard at `/admin/`. It uses:
- Go `html/template` for server-side rendering
- htmx for dynamic updates (no JavaScript framework)
- Minimal custom CSS — no Tailwind, no build step

### Pages
- `/admin/` — Dashboard: active sandbox count, resource pool usage, recent activity
- `/admin/sandboxes` — List all sandboxes (filterable by status, owner, template)
- `/admin/sandboxes/{id}` — Sandbox detail: status, metrics, audit log, actions (pause/resume/destroy)
- `/admin/hosts` — Host agent list with capacity and health
- `/admin/keys` — API key management

### htmx Patterns
- Sandbox list auto-refreshes via `hx-trigger="every 5s"`
- Actions (pause, resume, destroy) use `hx-post` with `hx-swap="outerHTML"` to update the row
- Audit log on sandbox detail uses `hx-get` with infinite scroll
- Metrics cards use `hx-trigger="every 10s"` for live updates

### Styling
Wrenn brand colors:
- Background: obsidian (#0c0c0c, #131313, #1a1a1a for raised surfaces)
- Text: warm off-white (#e8e6e3), dim (#9a9890)
- Accent: sage green (#8fbc8f)
- Borders: #2a2a2a
- Font: system monospace for data, system sans-serif for prose
- Minimal, developer-tool aesthetic. Dense, functional, sharp edges.

## Proto Definitions

### hostagent.proto (control plane ↔ host agent)
```protobuf
syntax = "proto3";
package hostagent;
option go_package = "github.com/wrenn-dev/wrenn-sandbox/proto/hostagent/gen";

service HostAgentService {
  rpc CreateSandbox(CreateSandboxRequest) returns (CreateSandboxResponse);
  rpc DestroySandbox(DestroySandboxRequest) returns (DestroySandboxResponse);
  rpc PauseSandbox(PauseSandboxRequest) returns (PauseSandboxResponse);
  rpc ResumeSandbox(ResumeSandboxRequest) returns (ResumeSandboxResponse);
  rpc Exec(ExecRequest) returns (stream ExecOutput);
  rpc WriteFile(WriteFileRequest) returns (WriteFileResponse);
  rpc ReadFile(ReadFileRequest) returns (ReadFileResponse);
  rpc Heartbeat(HeartbeatRequest) returns (HeartbeatResponse);
}
```

### envd protos (host agent ↔ guest agent)
Extracted from E2B's spec/ directory. ProcessService and FilesystemService. Do not modify these unless you also modify envd.

## REST API

All endpoints under `/v1/`. JSON request/response. API key auth via `X-API-Key` header.

```
POST   /v1/sandboxes                    Create sandbox
GET    /v1/sandboxes                    List sandboxes
GET    /v1/sandboxes/{id}               Get sandbox status
POST   /v1/sandboxes/{id}/exec          Execute command
PUT    /v1/sandboxes/{id}/files         Upload file
GET    /v1/sandboxes/{id}/files/{path}  Download file
POST   /v1/sandboxes/{id}/pause         Pause sandbox
POST   /v1/sandboxes/{id}/resume        Resume sandbox
DELETE /v1/sandboxes/{id}               Destroy sandbox
WS     /v1/sandboxes/{id}/terminal      Interactive terminal

GET    /v1/hosts                        List hosts (admin)
GET    /v1/keys                         List API keys (admin)
POST   /v1/keys                         Create API key (admin)
```

## Coding Conventions

### Go Style
- Follow standard Go conventions. Run `gofmt` and `go vet`.
- Use `context.Context` everywhere. Pass it through the full call chain.
- Error handling: wrap errors with `fmt.Errorf("create sandbox: %w", err)`. No bare returns.
- Logging: use `slog` (Go 1.21+ structured logging). No third-party loggers.
- No global state. Everything injected via constructors.

### Naming
- Sandbox IDs: `sb-` prefix + 8 hex chars (e.g., `sb-a1b2c3d4`)
- API keys: `wrn_` prefix + 32 random chars
- Host IDs: hostname or `host-` prefix + 8 hex chars
- TAP devices: `tap-` + first 8 chars of sandbox ID
- Network slot index: 1-based, determines all per-sandbox IPs

### Error Responses
```json
{
  "error": {
    "code": "pool_exhausted",
    "message": "Your vCPU pool is fully allocated. Upgrade your plan or destroy idle sandboxes."
  }
}
```

### Testing
- Unit tests: `go test ./internal/...`
- Integration tests: `go test ./tests/integration/...` (require running host agent + Firecracker)
- Table-driven tests for handlers and state machine transitions


## envd — Standalone Binary

envd is a **completely independent Go project**. It has its own `go.mod`, its own dependencies, and its own build. It is never imported by the control plane or host agent as a Go package. The only connection is the protobuf contract — both envd and the host agent generate code from the same `.proto` files.

**Why standalone:** envd runs inside microVMs. It gets compiled once as a static binary, baked into rootfs images, and then used across thousands of sandboxes. It has zero runtime dependency on the rest of the Wrenn codebase. The host agent talks to it over HTTP/Connect RPC via TAP networking — same as talking to any remote service.

**envd's own structure:**
```
envd/
├── go.mod                    # module github.com/wrenn-dev/envd (NOT the parent module)
├── go.sum
├── Makefile                  # self-contained build
├── main.go                   # Entry point, boots as PID 1
└── internal/
    ├── server/               # gRPC service implementations
    ├── process/              # Process exec, PTY, signal handling
    ├── filesystem/           # File read/write/list/watch
    └── network/              # Guest-side network config on boot/resume
```

**Building envd:**
```bash
cd envd
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o envd .
file envd  # MUST say "statically linked"
# Binary goes into rootfs images at /usr/local/bin/envd
```

**Versioning:** envd has its own version, independent of the control plane or host agent. When you update envd, you rebuild rootfs images. Existing sandboxes keep the old envd.

## Build Commands (Makefile)

```makefile
# ═══════════════════════════════════════════════════
#  Variables
# ═══════════════════════════════════════════════════
DATABASE_URL   ?= postgres://wrenn:wrenn@localhost:5432/wrenn?sslmode=disable
GOBIN          := $(shell pwd)/bin
ENVD_DIR       := envd
LDFLAGS        := -s -w

# ═══════════════════════════════════════════════════
#  Build
# ═══════════════════════════════════════════════════
.PHONY: build build-cp build-agent build-envd

build: build-cp build-agent build-envd

build-cp:
	go build -v -ldflags="$(LDFLAGS)" -o $(GOBIN)/wrenn-cp ./cmd/control-plane

build-agent:
	go build -v -ldflags="$(LDFLAGS)" -o $(GOBIN)/wrenn-agent ./cmd/host-agent

build-envd:
	cd $(ENVD_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -ldflags="$(LDFLAGS)" -o ../$(GOBIN)/envd .
	@file $(GOBIN)/envd | grep -q "statically linked" || \
		(echo "ERROR: envd is not statically linked!" && exit 1)

# ═══════════════════════════════════════════════════
#  Development
# ═══════════════════════════════════════════════════
.PHONY: dev dev-cp dev-agent dev-envd dev-infra dev-down dev-seed

## One command to start everything for local dev
dev: dev-infra migrate-up dev-seed dev-cp

dev-infra:
	docker compose -f deploy/docker-compose.dev.yml up -d
	@echo "Waiting for PostgreSQL..."
	@until pg_isready -h localhost -p 5432 -q; do sleep 0.5; done
	@echo "Dev infrastructure ready."

dev-down:
	docker compose -f deploy/docker-compose.dev.yml down -v

dev-cp:
	@if command -v air > /dev/null; then air -c .air.cp.toml; \
	else go run ./cmd/control-plane; fi

dev-agent:
	sudo go run ./cmd/host-agent

dev-envd:
	cd $(ENVD_DIR) && go run . --debug --listen-tcp :3002

dev-seed:
	go run ./scripts/seed.go

# ═══════════════════════════════════════════════════
#  Database (goose)
# ═══════════════════════════════════════════════════
.PHONY: migrate-up migrate-down migrate-status migrate-create migrate-reset

migrate-up:
	goose -dir db/migrations postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir db/migrations postgres "$(DATABASE_URL)" down

migrate-status:
	goose -dir db/migrations postgres "$(DATABASE_URL)" status

migrate-create:
	goose -dir db/migrations create $(name) sql

migrate-reset:
	goose -dir db/migrations postgres "$(DATABASE_URL)" reset
	goose -dir db/migrations postgres "$(DATABASE_URL)" up

# ═══════════════════════════════════════════════════
#  Code Generation
# ═══════════════════════════════════════════════════
.PHONY: generate proto sqlc

generate: proto sqlc

proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/hostagent/hostagent.proto
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/envd/process.proto proto/envd/filesystem.proto

sqlc:
	@if command -v sqlc > /dev/null; then sqlc generate; \
	else echo "sqlc not installed, skipping"; fi

# ═══════════════════════════════════════════════════
#  Quality & Testing
# ═══════════════════════════════════════════════════
.PHONY: fmt lint vet test test-integration test-all tidy check

fmt:
	gofmt -w .
	cd $(ENVD_DIR) && gofmt -w .

lint:
	golangci-lint run ./...

vet:
	go vet ./...
	cd $(ENVD_DIR) && go vet ./...

test:
	go test -race -v ./internal/...

test-integration:
	go test -race -v -tags=integration ./tests/integration/...

test-all: test test-integration

tidy:
	go mod tidy
	cd $(ENVD_DIR) && go mod tidy

## Run all quality checks in CI order
check: fmt vet lint test

# ═══════════════════════════════════════════════════
#  Rootfs Images
# ═══════════════════════════════════════════════════
.PHONY: images image-minimal image-python image-node

images: build-envd image-minimal image-python image-node

image-minimal:
	sudo bash images/templates/minimal/build.sh

image-python:
	sudo bash images/templates/python311/build.sh

image-node:
	sudo bash images/templates/node20/build.sh

# ═══════════════════════════════════════════════════
#  Deployment
# ═══════════════════════════════════════════════════
.PHONY: setup-host install

setup-host:
	sudo bash scripts/setup-host.sh

install: build
	sudo cp $(GOBIN)/wrenn-cp /usr/local/bin/
	sudo cp $(GOBIN)/wrenn-agent /usr/local/bin/
	sudo cp deploy/systemd/*.service /etc/systemd/system/
	sudo systemctl daemon-reload

# ═══════════════════════════════════════════════════
#  Clean
# ═══════════════════════════════════════════════════
.PHONY: clean

clean:
	rm -rf bin/
	cd $(ENVD_DIR) && rm -f envd

# ═══════════════════════════════════════════════════
#  Help
# ═══════════════════════════════════════════════════
.DEFAULT_GOAL := help
.PHONY: help
help:
	@echo "Wrenn Sandbox"
	@echo ""
	@echo "  make dev            Full local dev (infra + migrate + seed + control plane)"
	@echo "  make dev-infra      Start PostgreSQL + Prometheus + Grafana"
	@echo "  make dev-down       Stop dev infra"
	@echo "  make dev-cp         Control plane (hot reload if air installed)"
	@echo "  make dev-agent      Host agent (sudo required)"
	@echo "  make dev-envd       envd in TCP debug mode"
	@echo ""
	@echo "  make build          Build all binaries → bin/"
	@echo "  make build-envd     Build envd static binary"
	@echo ""
	@echo "  make migrate-up     Apply migrations"
	@echo "  make migrate-create name=xxx  New migration"
	@echo "  make migrate-reset  Drop + re-apply all"
	@echo ""
	@echo "  make generate       Proto + sqlc codegen"
	@echo "  make check          fmt + vet + lint + test"
	@echo "  make test-all       Unit + integration tests"
	@echo ""
	@echo "  make images         Build all rootfs images"
	@echo "  make setup-host     One-time host setup"
	@echo "  make install        Install binaries + systemd units"
```

### docker-compose.dev.yml

```yaml
# deploy/docker-compose.dev.yml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: wrenn
      POSTGRES_PASSWORD: wrenn
      POSTGRES_DB: wrenn
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data

  prometheus:
    image: prom/prometheus:latest
    ports:
      - "9090:9090"
    volumes:
      - ./deploy/prometheus.yml:/etc/prometheus/prometheus.yml

  grafana:
    image: grafana/grafana:latest
    ports:
      - "3001:3000"
    environment:
      GF_SECURITY_ADMIN_PASSWORD: admin

volumes:
  pgdata:
```

### .env.example

```bash
# Database
DATABASE_URL=postgres://wrenn:wrenn@localhost:5432/wrenn?sslmode=disable

# Control Plane
CP_LISTEN_ADDR=:8000
CP_HOST_AGENT_ADDR=localhost:50051

# Host Agent
AGENT_LISTEN_ADDR=:50051
AGENT_KERNEL_PATH=/var/lib/wrenn/kernels/vmlinux
AGENT_IMAGES_PATH=/var/lib/wrenn/images
AGENT_SANDBOXES_PATH=/var/lib/wrenn/sandboxes
AGENT_HOST_INTERFACE=eth0

# Lago (billing — external service)
LAGO_API_URL=http://localhost:3000
LAGO_API_KEY=

# Object Storage (hibernate snapshots — Hetzner Object Storage, S3-compatible)
# Hetzner Object Storage uses the S3-compatible API, so we use standard AWS SDK environment variables
S3_BUCKET=wrenn-snapshots
S3_REGION=fsn1
S3_ENDPOINT=https://fsn1.your-objectstorage.com
AWS_ACCESS_KEY_ID=       # Hetzner Object Storage access key (S3-compatible)
AWS_SECRET_ACCESS_KEY=   # Hetzner Object Storage secret key (S3-compatible)
```

### Development Workflow

```bash
# First time
git clone https://github.com/wrenn-dev/wrenn-sandbox && cd wrenn-sandbox
make tidy

# Install tools
go install github.com/pressly/goose/v3/cmd/goose@latest
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
go install github.com/air-verse/air@latest
go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

# Start everything
make dev-infra          # PostgreSQL + monitoring
make migrate-up         # Create tables
make dev-seed           # Test API key

# Terminal 1
make dev-cp             # → http://localhost:8000 (API + admin UI)

# Terminal 2
make dev-agent          # → gRPC on :50051

# Terminal 3
curl http://localhost:8000/v1/sandboxes
open http://localhost:8000/admin/
```

## Implementation Priority

### Phase 1: Boot a VM
1. Build envd static binary
2. Create minimal rootfs with envd baked in
3. Write `internal/vm/` — boot Firecracker
4. Write `internal/envdclient/` — connect to envd over TAP network
5. Test: boot VM, run "echo hello", get output back

### Phase 2: Host Agent
1. Write `internal/network/` — TAP + NAT per sandbox
2. Write `internal/filesystem/` — CoW rootfs clones
3. Define hostagent.proto, generate stubs
4. Write host agent gRPC server
5. Test: grpcurl to create/exec/destroy

### Phase 3: Control Plane
1. Set up PostgreSQL, write goose migrations
2. Write `internal/api/` — REST handlers
3. Write `internal/auth/` — API key validation
4. Write `internal/scheduler/` — SingleHostScheduler
5. Test: curl to create/exec/destroy via REST

### Phase 4: Admin UI
1. Write `internal/admin/` — htmx templates
2. Dashboard, sandbox list, sandbox detail
3. Host status, API key management
4. Test: browser, see sandboxes, perform actions

### Phase 5: Persistence
1. Write `internal/snapshot/` — Firecracker snapshots
2. Add pause/hibernate/resume states
3. Write `internal/lifecycle/` — auto-pause idle sandboxes
4. Test: pause, resume, verify state intact

### Phase 6: SDKs
1. Python SDK
2. TypeScript SDK
3. Go SDK
4. Test: end-to-end from SDK

### Phase 7: Hardening
1. Jailer integration
2. cgroup resource limits
3. Egress filtering
4. Prometheus metrics
5. Stress testing

## Dependencies

### Go modules (main project)
```
github.com/go-chi/chi/v5
github.com/jackc/pgx/v5
github.com/pressly/goose/v3
github.com/firecracker-microvm/firecracker-go-sdk
github.com/vishvananda/netlink
google.golang.org/grpc
google.golang.org/protobuf
github.com/prometheus/client_golang
github.com/gorilla/websocket
github.com/rs/cors
golang.org/x/crypto
```

### envd Go modules (separate go.mod — minimal deps only)
```
google.golang.org/grpc
google.golang.org/protobuf
github.com/vishvananda/netlink
```

### External services
- PostgreSQL (local Docker or managed)
- Lago (billing, HTTP API only)
- S3/GCS (hibernate snapshot storage)

### Dev tools
```
goose, protoc, protoc-gen-go, protoc-gen-go-grpc, air, golangci-lint, grpcurl, sqlc
```

## Important Notes

- Host agent MUST run as root (NET_ADMIN + /dev/kvm).
- Control plane does NOT need root.
- envd is a **standalone Go module** (`envd/go.mod`). Never imported by other Go code. Static binary. Baked into rootfs images.
- `make dev` is the one command for local development.
- For dev without Firecracker, `make dev-envd` runs envd in TCP mode.