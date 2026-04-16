# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Wrenn Sandbox is a microVM-based code execution platform. Users create isolated sandboxes (Firecracker microVMs), run code inside them, and get output back via SDKs. Think E2B but with persistent sandboxes, pool-based pricing, and a single-binary deployment story.

## Build & Development Commands

All commands go through the Makefile. Never use raw `go build` or `go run`.

```bash
make build              # Build all binaries → builds/
make build-cp           # Control plane only (builds frontend first)
make build-agent        # Host agent only
make build-envd         # envd static binary (verified statically linked)
make build-frontend     # SvelteKit dashboard → internal/dashboard/static/

make dev                # Full local dev: infra + migrate + control plane
make dev-infra          # Start PostgreSQL + Prometheus + Grafana (Docker)
make dev-down           # Stop dev infra
make dev-cp             # Control plane with hot reload (if air installed)
make dev-frontend       # Vite dev server with HMR (port 5173)
make dev-agent          # Host agent (sudo required)
make dev-envd           # envd in TCP debug mode

make check              # fmt + vet + lint + test (CI order)
make test               # Unit tests: go test -race -v ./internal/...
make test-integration   # Integration tests (require host agent + Firecracker)
make fmt                # gofmt both modules
make vet                # go vet both modules
make lint               # golangci-lint

make migrate-up         # Apply pending migrations
make migrate-down       # Rollback last migration
make migrate-create name=xxx  # Scaffold new goose migration (never create manually)
make migrate-reset      # Drop + re-apply all

make generate           # Proto (buf) + sqlc codegen
make proto              # buf generate for all proto dirs
make tidy               # go mod tidy both modules
```

Run a single test: `go test -race -v -run TestName ./internal/path/...`

## Architecture

```
User SDK → HTTPS/WS → Control Plane → Connect RPC → Host Agent → HTTP/Connect RPC over TAP → envd (inside VM)
```

**Three binaries, two Go modules:**

| Binary | Module | Entry point | Runs as |
|--------|--------|-------------|---------|
| wrenn-cp | `git.omukk.dev/wrenn/wrenn` | `cmd/control-plane/main.go` | Unprivileged |
| wrenn-agent | `git.omukk.dev/wrenn/wrenn` | `cmd/host-agent/main.go` | `wrenn` user with capabilities (SYS_ADMIN, NET_ADMIN, NET_RAW, SYS_PTRACE, KILL, DAC_OVERRIDE, MKNOD) via setcap; also accepts root |
| envd | `git.omukk.dev/wrenn/wrenn/envd` (standalone `envd/go.mod`) | `envd/main.go` | PID 1 inside guest VM |

envd is a **completely independent Go module**. It is never imported by the main module. The only connection is the protobuf contract. It compiles to a static binary baked into rootfs images.

**Key architectural invariant:** The host agent is **stateful** (in-memory `boxes` map is the source of truth for running VMs). The control plane is **stateless** (all persistent state in PostgreSQL). The reconciler (`internal/api/reconciler.go`) bridges the gap — it periodically compares DB records against the host agent's live state and marks orphaned sandboxes as "stopped".

### Control Plane

**Internal packages:** `internal/api/`, `internal/dashboard/`, `internal/email/`

**Public packages (importable by cloud repo):** `pkg/config/`, `pkg/db/`, `pkg/auth/`, `pkg/auth/oauth/`, `pkg/scheduler/`, `pkg/lifecycle/`, `pkg/channels/`, `pkg/audit/`, `pkg/service/`, `pkg/events/`, `pkg/id/`, `pkg/validate/`

**Extension framework:** `pkg/cpextension/` (shared `Extension` interface + `ServerContext`), `pkg/cpserver/` (exported `Run()` entrypoint with functional options for cloud `main.go`)

The cloud repo imports this module as a Go dependency and calls `cpserver.Run(cpserver.WithExtensions(myExt))`. Each extension implements two methods: `RegisterRoutes(r chi.Router, sctx ServerContext)` to add HTTP routes, and `BackgroundWorkers(sctx ServerContext) []func(context.Context)` to add long-running goroutines. `ServerContext` carries all OSS services (DB, scheduler, auth, etc.) so extensions can use them without reimplementing anything. To expose a new OSS service to extensions, add it to `ServerContext` in `pkg/cpextension/extension.go` and populate it in `pkg/cpserver/run.go`.

**pkg/ vs internal/ decision rule:** A package belongs in `pkg/` only if the cloud repo needs to import it directly. Everything else stays in `internal/`. New OSS services (e.g. email, notifications) go in `internal/` — the cloud repo accesses them through `ServerContext`, not by importing the package. Do not put a service in `pkg/` just because the cloud repo uses it.

Startup (`cmd/control-plane/main.go`) is a thin wrapper: `cpserver.Run(cpserver.WithVersion(...))`. All 20 initialization steps live in `pkg/cpserver/run.go`: config → pgxpool → `db.Queries` → Redis → mTLS CA → host client pool → scheduler → OAuth → channels → audit logger → `api.New()` → background workers → HTTP server. Everything flows through constructor injection.

- **API Server** (`internal/api/server.go`): chi router with middleware. Creates handler structs (`sandboxHandler`, `execHandler`, `filesHandler`, etc.) injected with `db.Queries` and the host agent Connect RPC client. Routes under `/v1/capsules/*`. Accepts `[]cpextension.Extension` — each extension's `RegisterRoutes()` is called after all core routes are registered.
- **Reconciler** (`internal/api/reconciler.go`): background goroutine (every 30s) that compares DB records against `agent.ListSandboxes()` RPC. Marks orphaned DB entries as "stopped".
- **Dashboard** (SvelteKit + Tailwind + Bits UI, statically built and embedded via `go:embed`, served as catch-all at root)
- **Database**: PostgreSQL via pgx/v5. Queries generated by sqlc from `db/queries/*.sql` → `pkg/db/`. Migrations in `db/migrations/` (goose, plain SQL). `db/migrations/embed.go` exposes `migrations.FS` so the cloud repo can run OSS migrations via `go:embed`.
- **Config** (`pkg/config/config.go`): purely environment variables (`DATABASE_URL`, `CP_LISTEN_ADDR`, `CP_HOST_AGENT_ADDR`), no YAML/file config.

### Host Agent

**Packages:** `internal/hostagent/`, `internal/sandbox/`, `internal/vm/`, `internal/network/`, `internal/devicemapper/`, `internal/envdclient/`, `internal/snapshot/`

**Production deployment:** `scripts/prepare-wrenn-user.sh` creates the `wrenn` system user, sets Linux capabilities (setcap) on wrenn-agent and all child binaries (iptables, losetup, dmsetup, etc.), installs an apt hook to restore capabilities after package updates, configures udev rules for `/dev/net/tun`, loads required kernel modules, and writes systemd unit files for both services. No sudo grants — all privilege is via capabilities.

Startup (`cmd/host-agent/main.go`) wires: root/capabilities check → enable IP forwarding → clean up stale dm devices → `sandbox.Manager` (containing `vm.Manager` + `network.SlotAllocator` + `devicemapper.LoopRegistry`) → `hostagent.Server` (Connect RPC handler) → HTTP server.

- **RPC Server** (`internal/hostagent/server.go`): implements `hostagentv1connect.HostAgentServiceHandler`. Thin wrapper — every method delegates to `sandbox.Manager`. Maps Connect error codes on return.
- **Sandbox Manager** (`internal/sandbox/manager.go`): the core orchestration layer. Maintains in-memory state in `boxes map[string]*sandboxState` (protected by `sync.RWMutex`). Each `sandboxState` holds a `models.Sandbox`, a `*network.Slot`, and an `*envdclient.Client`. Runs a TTL reaper (every 10s) that auto-destroys timed-out sandboxes.
- **VM Manager** (`internal/vm/manager.go`, `fc.go`, `config.go`): manages Firecracker processes. Uses raw HTTP API over Unix socket (`/tmp/fc-{sandboxID}.sock`), not the firecracker-go-sdk Machine type. Launches Firecracker via `unshare -m` + `ip netns exec`. Configures VM via PUT to `/boot-source`, `/drives/rootfs`, `/network-interfaces/eth0`, `/machine-config`, then starts with PUT `/actions`.
- **Network** (`internal/network/setup.go`, `allocator.go`): per-sandbox network namespace with veth pair + TAP device. See Networking section below.
- **Device Mapper** (`internal/devicemapper/devicemapper.go`): CoW rootfs via device-mapper snapshots. Shared read-only loop devices per base template (refcounted `LoopRegistry`), per-sandbox sparse CoW files, dm-snapshot create/restore/remove/flatten operations.
- **envd Client** (`internal/envdclient/client.go`, `health.go`): dual interface to the guest agent. Connect RPC for streaming process exec (`process.Start()` bidirectional stream). Plain HTTP for file operations (POST/GET `/files?path=...&username=root`). Health check polls `GET /health` every 100ms until ready (30s timeout).

### envd (Guest Agent)

**Module:** `envd/` with its own `go.mod` (`git.omukk.dev/wrenn/wrenn/envd`)

Runs as PID 1 inside the microVM via `wrenn-init.sh` (mounts procfs/sysfs/dev, sets hostname, writes resolv.conf, then execs envd). Extracted from E2B (Apache 2.0), with shared packages internalized into `envd/internal/shared/`. Listens on TCP `0.0.0.0:49983`.

- **ProcessService**: start processes, stream stdout/stderr, signal handling, PTY support
- **FilesystemService**: stat/list/mkdir/move/remove/watch files
- **Health**: GET `/health`

### Dashboard (Frontend)

**Directory:** `frontend/` — standalone SvelteKit app (Svelte 5, runes mode)

- **Stack**: SvelteKit + `adapter-static` + Tailwind CSS v4 + Bits UI (headless accessible components)
- **Package manager**: pnpm
- **Routing**: SvelteKit file-based routing under `frontend/src/routes/`
- **Routing layout**: `/login` and `/signup` at root, authenticated pages under `/dashboard/*` (e.g. `/dashboard/capsules`, `/dashboard/keys`)
- **Build output**: `frontend/build/` → copied to `internal/dashboard/static/` → embedded via `go:embed` into the control plane binary
- **Serving**: `internal/dashboard/dashboard.go` registers a `NotFound` catch-all SPA handler with fallback to `index.html`. API routes (`/v1/*`, `/openapi.yaml`, `/docs`) are registered first and take priority
- **Dev workflow**: `make dev-frontend` runs Vite dev server on port 5173 with HMR. API calls proxy to `http://localhost:8000`
- **Fonts**: Manrope (UI), Instrument Serif (headings), JetBrains Mono (code), Alice (brand wordmark) — all self-hosted via `@fontsource`
- **Dark mode**: class-based (`.dark` on `<html>`) with system preference detection + localStorage persistence

To add a new page: create `frontend/src/routes/your-page/+page.svelte`.

### Networking (per sandbox)

Each sandbox gets its own Linux network namespace (`ns-{idx}`). Slot index (1-based, up to 65534) determines all addressing:

```
Host Namespace                      Namespace "ns-{idx}"                   Guest VM
──────────────────────────────────────────────────────────────────────────────────────
veth-{idx}  ←──── veth pair ────→  eth0
10.12.0.{idx*2}/31                 10.12.0.{idx*2+1}/31
                                     │
                                   tap0 (169.254.0.22/30) ←── TAP ──→ eth0 (169.254.0.21)
                                                                          ↑ kernel ip= boot arg
```

- **Host-reachable IP**: `10.11.0.{idx}/32` — routed through veth to namespace, DNAT'd to guest
- **Outbound NAT**: guest (169.254.0.21) → SNAT to vpeerIP inside namespace → MASQUERADE on host to default interface
- **Inbound NAT**: host traffic to 10.11.0.{idx} → DNAT to 169.254.0.21 inside namespace
- IP forwarding enabled inside each namespace
- All details in `internal/network/setup.go`

### Sandbox State Machine
```
PENDING → STARTING → RUNNING → PAUSED → HIBERNATED
                       │          │
                       ↓          ↓
                    STOPPED    STOPPED → (destroyed)

Any state → ERROR (on crash/failure)
PAUSED → RUNNING (warm snapshot resume)
HIBERNATED → RUNNING (cold snapshot resume, slower)
```

### Key Request Flows

**Sandbox creation** (`POST /v1/capsules`):
1. API handler generates sandbox ID, inserts into DB as "pending"
2. RPC `CreateSandbox` → host agent → `sandbox.Manager.Create()`
3. Manager: resolve base rootfs → acquire shared loop device → create dm-snapshot (sparse CoW file) → allocate network slot → `CreateNetwork()` (netns + veth + tap + NAT) → `vm.Create()` (start Firecracker with `/dev/mapper/wrenn-{id}`, configure via HTTP API, boot) → `envdclient.WaitUntilReady()` (poll /health) → store in-memory state
4. API handler updates DB to "running" with host_ip

**Command execution** (`POST /v1/capsules/{id}/exec`):
1. API handler verifies sandbox is "running" in DB
2. RPC `Exec` → host agent → `sandbox.Manager.Exec()` → `envdclient.Exec()`
3. envd client opens bidirectional Connect RPC stream (`process.Start`), collects stdout/stderr/exit_code
4. API handler checks UTF-8 validity (base64-encodes if binary), updates last_active_at, returns result

**Streaming exec** (`WS /v1/capsules/{id}/exec/stream`):
1. WebSocket upgrade, read first message for cmd/args
2. RPC `ExecStream` → host agent → `sandbox.Manager.ExecStream()` → `envdclient.ExecStream()`
3. envd client returns a channel of events; host agent forwards events through the RPC stream
4. API handler forwards stream events to WebSocket as JSON messages (`{type: "stdout"|"stderr"|"exit", ...}`)

**File transfer**: Write uses multipart POST to envd `/files`; read uses GET. Streaming variants chunk in 64KB pieces through the RPC stream.

## REST API

Routes defined in `internal/api/server.go`, handlers in `internal/api/handlers_*.go`. OpenAPI spec embedded via `//go:embed` and served at `/openapi.yaml` (Swagger UI at `/docs`). JSON request/response. API key auth via `X-API-Key` header. Error responses: `{"error": {"code": "...", "message": "..."}}`.

## Code Generation

### Proto (Connect RPC)

Proto source of truth is `proto/envd/*.proto` and `proto/hostagent/*.proto`. Run `make proto` to regenerate. Three `buf.gen.yaml` files control output:

| buf.gen.yaml location | Generates to | Used by |
|---|---|---|
| `proto/envd/buf.gen.yaml` | `proto/envd/gen/` | Main module (host agent's envd client) |
| `proto/hostagent/buf.gen.yaml` | `proto/hostagent/gen/` | Main module (control plane ↔ host agent) |
| `envd/spec/buf.gen.yaml` | `envd/internal/services/spec/` | envd module (guest agent server) |

The envd `buf.gen.yaml` reads from `../../proto/envd/` (same source protos) but generates into envd's own module. This means the same `.proto` files produce two independent sets of Go stubs — one for each Go module.

To add a new RPC method: edit the `.proto` file → `make proto` → implement the handler on both sides.

### sqlc

Config: `sqlc.yaml` (project root). Reads queries from `db/queries/*.sql`, reads schema from `db/migrations/`, outputs to `pkg/db/`.

To add a new query: add it to the appropriate `.sql` file in `db/queries/` → `make generate` → use the new method on `*db.Queries`.

## Key Technical Decisions

- **Connect RPC** (not gRPC) for all RPC communication between components
- **Buf + protoc-gen-connect-go** for code generation (not protoc-gen-go-grpc)
- **Raw Firecracker HTTP API** via Unix socket (not firecracker-go-sdk Machine type)
- **TAP networking** (not vsock) for host-to-envd communication
- **Device-mapper snapshots** for rootfs CoW — shared read-only loop device per base template, per-sandbox sparse CoW file, Firecracker gets `/dev/mapper/wrenn-{id}`
- **PostgreSQL** via pgx/v5 + sqlc (type-safe query generation). Goose for migrations (plain SQL, up/down)
- **Dashboard**: SvelteKit (Svelte 5, adapter-static) + Tailwind CSS v4 + Bits UI. Built to static files, embedded into the Go binary via `go:embed`, served as catch-all at root
- **Lago** for billing (external service, not in this codebase)

## Coding Conventions

- **Go style**: `gofmt`, `go vet`, `context.Context` everywhere, errors wrapped with `fmt.Errorf("action: %w", err)`, `slog` for logging, no global state
- **Naming**: Sandbox IDs `sb-` + 8 hex, API keys `wrn_` + 32 chars, Host IDs `host-` + 8 hex
- **Dependencies**: Use `go get` to add deps, never hand-edit go.mod. For envd deps: `cd envd && go get ...` (separate module)
- **Generated code**: Always commit generated code (proto stubs, sqlc). Never add generated code to .gitignore
- **Migrations**: Always use `make migrate-create name=xxx`, never create migration files manually
- **Testing**: Table-driven tests for handlers and state machine transitions

### Two-module gotcha

The main module (`go.mod`) and envd (`envd/go.mod`) are fully independent. `make tidy`, `make fmt`, `make vet` already operate on both. But when adding dependencies manually, remember to target the correct module (`cd envd && go get ...` for envd deps). `make proto` also generates stubs for both modules from the same proto sources.

## Rootfs & Guest Init

- **wrenn-init** (`images/wrenn-init.sh`): the PID 1 init script baked into every rootfs. Mounts virtual filesystems, sets hostname, writes `/etc/resolv.conf`, then execs envd.
- **Updating the rootfs** after changing envd or wrenn-init: `bash scripts/update-debug-rootfs.sh [rootfs_path]`. This builds envd via `make build-envd`, mounts the rootfs image, copies in the new binaries, and unmounts. Defaults to `/var/lib/wrenn/images/minimal.ext4`.
- Rootfs images are minimal debootstrap — no systemd, no coreutils beyond busybox. Use `/bin/sh -c` for shell builtins inside the guest.

## Fixed Paths (on host machine)

- Kernel: `/var/lib/wrenn/kernels/vmlinux`
- Base rootfs images: `/var/lib/wrenn/images/{template}.ext4`
- Sandbox clones: `/var/lib/wrenn/sandboxes/`
- Firecracker: `/usr/local/bin/firecracker` (e2b's fork of firecracker)

## Design Context

### Users
Developers across the full spectrum — solo engineers building side projects, startup teams integrating sandboxed execution into products, and platform/infra engineers at larger organizations running production workloads on Firecracker microVMs. They arrive with context: they know what a process is, what a rootfs is, what a TTY means. The interface must feel at home for all three: approachable enough not to intimidate a hacker, precise enough to earn the trust of a production ops team. Never condescend, never oversimplify. Trust the user to understand what they're looking at.

**Primary job to be done:** Understand what's running, act on it confidently, and get back to code.

### Brand Personality
**Precise. Warm. Uncompromising.**

Wrenn is an engineer's favorite tool — built with visible care, not assembled from defaults. It runs real infrastructure (Firecracker microVMs), so the UI should reflect that seriousness without becoming cold or corporate. The warmth comes from the typography and color palette; the precision comes from hierarchy, density, and data fidelity.

Emotional goal: **in control.** Users leave a session with full confidence in what's running, what happened, and what comes next. Nothing is hidden, nothing is ambiguous.

### Aesthetic Direction
**Dark-only (permanently), industrial-warm, data-forward.**

No light mode planned. All design decisions should optimize for dark. The near-black-green background palette (`#0a0c0b` through `#2a302d`) reads as "black with intention" — not pitch black (cold) and not charcoal (dated). The sage green accent (`#5e8c58`) is muted and organic, a meaningful departure from the startup-green neon that saturates the developer tool space.

**Anti-references:**
- **Supabase**: avoid the friendly, approachable startup-green energy — too generic, too eager to please
- **AWS / GCP consoles**: avoid utility-first density without craft — functional but joyless, visually dated

**References that capture the right spirit:**
- The precision of a well-calibrated instrument
- Editorial typography from technical publications
- The quiet confidence of tools that don't need to explain themselves

### Type System
Four fonts with strict roles — this is the design system's strongest personality trait and must be respected:

| Font | CSS Class | Role | When to use |
|------|-----------|------|-------------|
| **Manrope** (variable, sans) | `font-sans` | UI workhorse | All body copy, nav, labels, buttons, form text |
| **Instrument Serif** | `font-serif` | Display / editorial | Page titles (h1), dialog headings, metric values, hero moments |
| **JetBrains Mono** (variable) | `font-mono` | Data / code | IDs, timestamps, key prefixes, file paths, terminal output, metrics |
| **Alice** | brand wordmark only | Brand wordmark | "Wrenn" in sidebar and login only — nowhere else |

Instrument Serif at scale creates the signature editorial moments. Mono provides the precision signal for technical data. Never swap these roles.

**Tracking overrides (app.css):**
- `.font-serif` — `letter-spacing: 0.015em` (positive tracking; Instrument Serif reads less condensed at display sizes)
- `.font-mono` — `font-variant-numeric: tabular-nums` (numbers align in tables and metric displays)

**Type scale (root: 87.5% = 14px base):**
| Token | Value | Use |
|---|---|---|
| `--text-display` | 2.571rem (~36px) | Auth section headings |
| `--text-page` | 2rem (~28px) | Page h1 titles |
| `--text-heading` | 1.429rem (~20px) | Dialog headings, empty states |
| `--text-body` | 1rem (~14px) | Primary body, buttons, inputs |
| `--text-ui` | 0.929rem (~13px) | Nav labels, table cells |
| `--text-meta` | 0.857rem (~12px) | Key prefixes, minor info |
| `--text-label` | 0.786rem (~11px) | Uppercase section labels |
| `--text-badge` | 0.714rem (~10px) | Live badges, tiny indicators |

### Color System

All values are CSS custom properties in `frontend/src/app.css`.

**Backgrounds (6-step near-black-green scale):**
| Token | Value | Use |
|---|---|---|
| `--color-bg-0` | `#0a0c0b` | Page base, sidebar deepest layer |
| `--color-bg-1` | `#0f1211` | Sidebar surface |
| `--color-bg-2` | `#141817` | Card backgrounds |
| `--color-bg-3` | `#1a1e1c` | Table headers, elevated surfaces |
| `--color-bg-4` | `#212624` | Hover states, inputs |
| `--color-bg-5` | `#2a302d` | Highlighted items, selected rows |

**Text (5-level hierarchy):**
| Token | Value | Use |
|---|---|---|
| `--color-text-bright` | `#eae7e2` | H1s, dialog headings |
| `--color-text-primary` | `#d0cdc6` | Body copy, primary labels |
| `--color-text-secondary` | `#9b9790` | Secondary labels, descriptions |
| `--color-text-tertiary` | `#6b6862` | Hints, placeholders |
| `--color-text-muted` | `#454340` | Dividers as text, ultra-subtle |

**Accent (sage green — use sparingly, must feel earned):**
| Token | Value | Use |
|---|---|---|
| `--color-accent` | `#5e8c58` | Primary CTA, live indicators, focus rings, active nav |
| `--color-accent-mid` | `#89a785` | Hover accent text |
| `--color-accent-bright` | `#a4c89f` | Accent on dark backgrounds |
| `--color-accent-glow` | `rgba(94,140,88,0.07)` | Subtle tinted backgrounds |
| `--color-accent-glow-mid` | `rgba(94,140,88,0.14)` | Hover tint on accent items |

**Status semantics:**
| Token | Value | Use |
|---|---|---|
| `--color-amber` | `#d4a73c` | Warning, paused state |
| `--color-red` | `#cf8172` | Error, destructive actions |
| `--color-blue` | `#5a9fd4` | Info, neutral system states |

**Borders:** `--color-border` (`#1f2321`) default; `--color-border-mid` (`#2a2f2c`) for inputs/hover.

### Component Patterns

**Buttons:**
- Primary: solid sage green (`--color-accent`), hover brightness boost + micro-lift (`-translate-y-px`)
- Secondary: bordered (`--color-border-mid`), text transitions to accent on hover
- Danger: red text + subtle red background on hover
- All: `transition-all duration-150`

**Inputs:**
- Border `--color-border`, background `--color-bg-2`; focus transitions border and icon to accent
- Group focus pattern: `group` wrapper + `group-focus-within:text-[var(--color-accent)]` on icon

**Tables / data lists:**
- Grid layout; header `bg-3` + uppercase `--text-label`; row hover `hover:bg-[var(--color-bg-3)]`
- Status stripe: left border color matches sandbox state

**Status indicators:** Running = animated ping + sage green dot; Paused = amber dot; Stopped = muted gray. Color is never the sole differentiator.

**Modals & dialogs:** Border + shadow only — no accent gradient bars/strips. `fadeUp` 0.35s entrance.

**Empty states:** Large icon with glow, Instrument Serif heading, secondary body text, CTA below, `iconFloat` 4s animation.

**Animations (always respect `prefers-reduced-motion`):** `fadeUp` (entrance), `status-ping` (live indicator), `iconFloat` (empty states), `spin-once` (refresh), staggered `animation-delay` on lists.

### Design Principles

1. **Precision over friendliness.** Every element earns its place. Wrenn doesn't need to tell you it's developer-friendly — that should be self-evident from the quality of the information architecture.

2. **Density with breathing room.** Data-forward doesn't mean cramped. Strategic whitespace creates calm hierarchy within dense contexts. Sections breathe; rows don't waste space.

3. **Industrial warmth.** The serif + mono + warm-black combination prevents sterility. This is a forge, not a gallery. The warmth is in the details, not the primary colors.

4. **Legible at speed.** Users scan dashboards in seconds. Strong typographic contrast (serif h1, mono IDs, sans body), consistent patterns, and predictable placement let users orientate instantly without reading everything.

5. **Craft signals trust.** For infrastructure that runs production code, the quality of the UI is a proxy for the quality of the product. Pixel-level decisions matter. Polish is not decoration — it's a trust signal.
