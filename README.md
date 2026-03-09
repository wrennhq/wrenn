# Wrenn Sandbox

MicroVM-based code execution platform. Firecracker VMs, not containers. Pool-based pricing, persistent sandboxes, Python/TS/Go SDKs.

## Stack

| Component | Tech |
|---|---|
| Control plane | Go, chi, pgx, goose, htmx |
| Host agent | Go, Firecracker Go SDK, vsock |
| Guest agent (envd) | Go (extracted from E2B, standalone binary) |
| Database | PostgreSQL |
| Cache | Redis |
| Billing | Lago (external) |
| Snapshot storage | S3 (Seaweedfs for dev) |
| Monitoring | Prometheus + Grafana |
| Admin UI | htmx + Go html/template |

## Architecture

```
SDK → HTTPS → Control Plane → gRPC → Host Agent → vsock → envd (inside VM)
                  │                      │
                  ├── PostgreSQL          ├── Firecracker
                  ├── Redis               ├── TAP/NAT networking
                  └── Lago (billing)      ├── CoW rootfs clones
                                          └── Prometheus /metrics
```

Control plane is stateless (state in Postgres + Redis). Host agent is stateful (manages VMs on the local machine). envd is a static binary baked into rootfs images — separate Go module, separate build, never imported by anything.

## Prerequisites

- Linux with `/dev/kvm` (bare metal or nested virt)
- Go 1.22+
- Docker (for dev infra)
- Firecracker + jailer installed at `/usr/local/bin/`
- `protoc` + Go plugins for proto generation

```bash
# Firecracker
ARCH=$(uname -m) VERSION="v1.6.0"
curl -L "https://github.com/firecracker-microvm/firecracker/releases/download/${VERSION}/firecracker-${VERSION}-${ARCH}.tgz" | tar xz
sudo mv release-*/firecracker-* /usr/local/bin/firecracker
sudo mv release-*/jailer-* /usr/local/bin/jailer

# Go tools
go install github.com/pressly/goose/v3/cmd/goose@latest
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
go install github.com/air-verse/air@latest
go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

# KVM
ls /dev/kvm && sudo setfacl -m u:${USER}:rw /dev/kvm
```

## Quick Start

```bash
cp .env.example .env
make tidy
make dev-infra                # Postgres, Redis, Prometheus, Grafana
make migrate-up
make dev-seed

# Terminal 1
make dev-cp                   # :8000

# Terminal 2
make dev-agent                # :50051 (sudo)
```

- API: `http://localhost:8000/v1/sandboxes`
- Admin: `http://localhost:8000/admin/`
- Grafana: `http://localhost:3001` (admin/admin)
- Prometheus: `http://localhost:9090`

## Layout

```
cmd/
  control-plane/              REST API + admin UI + gRPC client + lifecycle manager
  host-agent/                 gRPC server + Firecracker + networking + metrics

envd/                         standalone Go module — separate go.mod, static binary
                              extracted from e2b-dev/infra, talks gRPC over vsock

proto/
  hostagent/                  control plane ↔ host agent
  envd/                       host agent ↔ guest agent (from E2B spec/)

internal/
  api/                        chi handlers
  admin/                      htmx + Go templates
  auth/                       API key + rate limiting
  scheduler/                  SingleHost → LeastLoaded
  lifecycle/                  auto-pause, auto-hibernate, auto-destroy
  vm/                         Firecracker config, boot, stop, jailer
  network/                    TAP, NAT, IP allocator (/30 subnets)
  filesystem/                 base images, CoW clones (cp --reflink)
  envdclient/                 vsock dialer + gRPC client to envd
  snapshot/                   pause/resume + S3 offload
  metrics/                    cgroup stats + Prometheus exporter
  models/                     Sandbox, Host structs
  config/                     env + YAML loading
  id/                         sb-xxxxxxxx generation

db/migrations/                goose SQL (00001_initial.sql, ...)
db/queries/                   raw SQL or sqlc

images/templates/             rootfs build scripts (minimal, python311, node20)
sdk/                          Python, TypeScript, Go client SDKs
deploy/                       systemd units, ansible, docker-compose.dev.yml
```

## Commands

```bash
# Dev
make dev                      # everything: infra + migrate + seed + control plane
make dev-infra                # just Postgres/Redis/Prometheus/Grafana
make dev-down                 # tear down
make dev-cp                   # control plane (hot reload with air)
make dev-agent                # host agent (sudo)
make dev-envd                 # envd in TCP debug mode (no Firecracker)
make dev-seed                 # test API key + data

# Build
make build                    # all → bin/
make build-envd               # static binary, verified

# DB
make migrate-up
make migrate-down
make migrate-create name=xxx
make migrate-reset            # drop + re-apply

# Codegen
make generate                 # proto + sqlc
make proto

# Quality
make check                    # fmt + vet + lint + test
make test                     # unit
make test-all                 # unit + integration
make tidy                     # go mod tidy (both modules)

# Images
make images                   # all rootfs (needs sudo + envd)

# Deploy
make setup-host               # one-time KVM/networking setup
make install                  # binaries + systemd
```

## Database

Postgres via pgx. No ORM. Migrations via goose (plain SQL).

Tables: `sandboxes`, `hosts`, `audit_events`, `api_keys`.

States: `pending → starting → running → paused → hibernated → stopped`. Any → `error`.

## envd

From [e2b-dev/infra](https://github.com/e2b-dev/infra) (Apache 2.0). PID 1 inside every VM. Exposes ProcessService + FilesystemService over gRPC on vsock.

Own `go.mod`. Must be `CGO_ENABLED=0`. Baked into rootfs at `/usr/local/bin/envd`. Kernel args: `init=/usr/local/bin/envd`.

Host agent connects via Firecracker vsock UDS using `CONNECT <port>\n` handshake.

## Networking

Each sandbox: `/30` from `10.0.0.0/16` (~16K per host).

```
Host: tap-sb-a1b2c3d4 (10.0.0.1/30) ↔ Guest eth0 (10.0.0.2/30)
NAT: iptables MASQUERADE via host internet interface
```

## Snapshots

- **Warm pause**: Firecracker snapshot on local NVMe. Resume <1s.
- **Cold hibernate**: zstd compressed, uploaded to S3/MinIO. Resume 5-10s.

## API

```
POST   /v1/sandboxes                 create
GET    /v1/sandboxes                 list
GET    /v1/sandboxes/{id}            status
POST   /v1/sandboxes/{id}/exec       exec
PUT    /v1/sandboxes/{id}/files      upload
GET    /v1/sandboxes/{id}/files/*    download
POST   /v1/sandboxes/{id}/pause      pause
POST   /v1/sandboxes/{id}/resume     resume
DELETE /v1/sandboxes/{id}            destroy
WS     /v1/sandboxes/{id}/terminal   shell
```

Auth: `X-API-Key` header. Prefix: `wrn_`.

## Phases

1. Boot VM + exec via vsock (W1)
2. Host agent + networking (W2)
3. Control plane + DB + REST (W3)
4. Admin UI / htmx (W4)
5. Pause / hibernate / resume (W5)
6. SDKs (W6)
7. Jailer, cgroups, egress, metrics (W7-8)