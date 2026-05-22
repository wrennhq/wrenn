# Wrenn

Secure infrastructure for AI

## Prerequisites

- Linux host with `/dev/kvm` access (bare metal or nested virt)
- Cloud Hypervisor binary at `/usr/local/bin/cloud-hypervisor`
- PostgreSQL
- Go 1.25+
- Rust 1.88+ with `x86_64-unknown-linux-musl` target (`rustup target add x86_64-unknown-linux-musl`)
- Bun (for frontend)
- Docker (for dev infra and rootfs builds)

## Build

```bash
make build    # outputs to builds/
```

Produces three binaries: `wrenn-cp` (control plane), `wrenn-agent` (host agent), `envd` (guest agent).

## Host setup

The host agent needs a kernel, the system base rootfs images, and working directories on the host machine.

### Directory structure

```
/var/lib/wrenn/
├── kernels/
│   └── vmlinux              # uncompressed Linux kernel (not bzImage)
├── images/
│   └── teams/
│       └── 0000000000000000000000000/   # platform team (base36 all-zeros)
│           ├── 0000000000000000000000000/rootfs.ext4   # minimal-ubuntu (id 0)
│           ├── 0000000000000000000000001/rootfs.ext4   # minimal-alpine (id 1)
│           ├── 0000000000000000000000002/rootfs.ext4   # minimal-arch   (id 2)
│           └── 0000000000000000000000003/rootfs.ext4   # minimal-fedora (id 3)
├── sandboxes/               # per-sandbox CoW files (created at runtime)
└── snapshots/               # pause/hibernate snapshot files (created at runtime)
```

Create the base directories (the per-template image dirs are created by the build scripts):

```bash
sudo mkdir -p /var/lib/wrenn/{kernels,images,sandboxes,snapshots}
```

### Kernel

Place an uncompressed `vmlinux` kernel at `/var/lib/wrenn/kernels/vmlinux`. Versioned kernels (`vmlinux-{semver}`) are also supported — the agent picks the latest by semver.

### System base rootfs images

There are four built-in **system base templates** — one per distro — that all other
templates snapshot from via device-mapper. They are platform-owned (visible to every
team) and protected from deletion (reserved template IDs 0–1024):

| Template | Distro | ID |
|----------|--------|----|
| `minimal-ubuntu` | `ubuntu:26.04` | 0 |
| `minimal-alpine` | `alpine:3.22` | 1 |
| `minimal-arch` | `archlinux:base` | 2 |
| `minimal-fedora` | `fedora:45` | 3 |

`minimal-ubuntu` is the default template for new sandboxes and builds. The same
statically-linked `envd` + `tini` run on all four regardless of the distro's libc
(glibc on Ubuntu/Arch/Fedora, musl on Alpine).

Each image contains these packages plus a `wrenn-user` account with passwordless `sudo`:

| Package | Why |
|---------|-----|
| `socat` | Bidirectional relay for port forwarding |
| `chrony` | Time sync from KVM PTP clock (`/dev/ptp0`) |
| `iproute2` (`iproute` on Fedora) | `ip` for guest network setup in `wrenn-init` |
| `tini` | PID 1 zombie reaper |
| `sudo` | User privilege management inside the guest |
| `wget` | HTTP fetching |
| `curl` | HTTP client |
| `ca-certificates` | TLS certificate verification |
| `git` | Version control |

**To build all four images** (each spawns a distro container, installs the packages +
`wrenn-user`, builds `envd`, injects `wrenn-init` + `tini`, and exports to the
team-scoped path). Requires Docker + sudo:

```bash
make images
```

Or build a single distro: `make rootfs-ubuntu` / `rootfs-alpine` / `rootfs-arch` / `rootfs-fedora`.

**To update the images** after changing `envd` or `wrenn-init.sh` (rebuilds `envd` once,
then re-injects `envd` + `wrenn-init` + `tini` into every system base image):

```bash
bash scripts/update-minimal-rootfs.sh
```

### IP forwarding

```bash
sudo sysctl -w net.ipv4.ip_forward=1
```

## Configure

Copy `.env.example` to `.env` and edit:

```bash
# Required
DATABASE_URL=postgres://wrenn:wrenn@localhost:5432/wrenn?sslmode=disable

# Control plane
WRENN_CP_LISTEN_ADDR=:8000
CP_HOST_AGENT_ADDR=http://localhost:50051

# Host agent
WRENN_HOST_LISTEN_ADDR=:50051
WRENN_DIR=/var/lib/wrenn
```

## Development

```bash
make dev          # Start PostgreSQL (Docker), run migrations, start control plane
make dev-agent    # Start host agent (separate terminal, sudo)
make dev-frontend # Vite dev server with HMR (port 5173)
make check        # fmt + vet + lint + test
```

### Host registration

Hosts must be registered with the control plane before they can serve sandboxes.

1. **Create a host record** in the dashboard (admin only — host management is not exposed over the SDK / API keys). Sign in at `/login`, open the admin hosts page, and click **Add host**. The dashboard returns a `registration_token` valid for 1 hour.

2. **Start the host agent** with the registration token and its externally-reachable address:
   ```bash
   sudo WRENN_CP_URL=http://localhost:8000 \
        ./builds/wrenn-agent \
        --register <token-from-step-1> \
        --address <host-ip>:50051
   ```
   On first startup the agent sends its specs (arch, CPU, memory, disk) to the control plane, receives a long-lived host JWT, and saves it to `$WRENN_DIR/host-token`.

3. **Subsequent startups** don't need `--register` — the agent loads the saved JWT automatically:
   ```bash
   sudo ./builds/wrenn-agent --address <host-ip>:50051
   ```

4. **If registration fails** (e.g., network error after token was consumed), regenerate a token from the dashboard host detail page, then restart the agent with the new token.

The agent sends heartbeats to the control plane every 30 seconds.

## Notification channels

Teams can subscribe to lifecycle events via webhook, Discord, Slack, Teams, Google Chat, Telegram, or Matrix. All providers consume the same event stream (durable Redis stream `wrenn:events`, consumer group `wrenn-channels-v1`, at-least-once delivery with two retries at 10s / 30s).

### Subscribable event types

| Event | Emitted on | Has outcome |
|-------|-----------|-------------|
| `capsule.create` | First boot of a sandbox | yes |
| `capsule.pause` | Manual pause, TTL auto-pause, or reconciler-detected pause | yes |
| `capsule.resume` | Unpause (any subsequent boot after `capsule.create`) | yes |
| `capsule.destroy` | Stop / destroy, including system cleanup-on-error | yes |
| `template.snapshot.create` | Snapshot taken from a running sandbox | yes |
| `template.snapshot.delete` | Snapshot deletion (including cleanup-on-error) | yes |
| `host.up` | Host agent comes online | no |
| `host.down` | Host agent crashes or misses heartbeats | no |

Subscribing to an event type delivers **both success and failure**. The `outcome` field on the payload (`success` or `error`) distinguishes them. `error` events carry an `error` string with the failure reason.

The transient `capsule.state.changed` event (intermediate transitions like `starting`, `pausing`, `resuming`) is **not** subscribable — it is delivered to the dashboard via SSE only and never written to the durable stream.

### Event payload

All channels receive the same canonical JSON shape:

```json
{
  "event": "capsule.pause",
  "outcome": "success",
  "timestamp": "2026-05-19T14:23:01Z",
  "team_id": "tm_...",
  "actor": {
    "type": "user",
    "id": "usr_...",
    "name": "alice@example.com"
  },
  "resource": {
    "id": "sb_a1b2c3d4",
    "type": "sandbox"
  },
  "metadata": {
    "reason": "ttl_expired"
  },
  "error": ""
}
```

| Field | Type | Notes |
|-------|------|-------|
| `event` | string | Event type (see table above) |
| `outcome` | `"success"` \| `"error"` \| `""` | Omitted for host.up/host.down |
| `timestamp` | RFC3339 UTC | When the event was published |
| `team_id` | string | Owning team |
| `actor.type` | `"user"` \| `"api_key"` \| `"system"` | System = TTL reaper, reconciler, cleanup-on-error |
| `actor.id` | string | User ID, API key ID, or empty for system |
| `actor.name` | string | Display name (email for user, label for api_key) |
| `resource.id` | string | Sandbox ID, snapshot ID, or host ID |
| `resource.type` | `"sandbox"` \| `"snapshot"` \| `"host"` | |
| `metadata` | object\<string,string\> | Event-specific context (e.g., `reason`, `from`/`to`, `inferred`) |
| `error` | string | Failure reason when `outcome == "error"` |

`metadata` keys you may observe:

- `reason` — `ttl_expired` (auto-pause), `orphaned` (reconciler cleanup), `cleanup_after_create_error`, `restored_after_host_recovery`, `host_state_sync`, `transient_timeout`, `transient_timeout_inferred`
- `inferred` — `"true"` when the reconciler derived the event from host state, not a direct host callback

### Webhook delivery

Webhook channels receive a raw `POST` with the JSON payload as the body.

Headers:

| Header | Value |
|--------|-------|
| `Content-Type` | `application/json` |
| `X-Wrenn-Delivery` | UUID, unique per delivery attempt |
| `X-Wrenn-Timestamp` | RFC3339 UTC, used for signature verification |
| `X-WRENN-SIGNATURE` | `sha256=<hex>` HMAC over `<timestamp>.<body>` using the channel's signing secret |

The signing secret is shown **once** at channel creation. Verify signatures by computing `HMAC-SHA256(secret, timestamp + "." + body)` and comparing to the header (constant-time compare). Reject deliveries where `X-Wrenn-Timestamp` is outside your acceptable clock skew window. Redirects are not followed.

Any non-2xx response triggers retry (10s, then 30s). After three total failures the event is dropped (logged on the control plane).

### Other providers

Discord, Slack, Teams, Google Chat, Telegram, and Matrix receive a formatted text message — the same fields, rendered as human-readable text — not the JSON payload. Use webhook if you need the structured event.

## Extending the control plane

The OSS control plane is designed to be embedded by a private cloud distribution without forking. Import this module, implement the `Extension` interface from `pkg/cpextension`, and pass it to `cpserver.Run`:

```go
import (
    "git.omukk.dev/wrenn/wrenn/pkg/cpextension"
    "git.omukk.dev/wrenn/wrenn/pkg/cpserver"
)

func main() {
    cpserver.Run(
        cpserver.WithVersion("cloud-1.0.0"),
        cpserver.WithExtensions(&myExtension{}),
    )
}
```

Every extension implements two methods:

```go
RegisterRoutes(r chi.Router, sctx cpextension.ServerContext)
BackgroundWorkers(sctx cpextension.ServerContext) []func(context.Context)
```

`ServerContext` exposes the initialized OSS services so extensions never re-implement them: `Queries`, `PgPool`, `Redis`, `HostPool`, `Scheduler`, `CA`, `Audit`, `Mailer`, `OAuthRegistry`, `Channels`, `ChannelPub`, `JWTSecret`, `Sessions`, `Config`.

### Optional hook interfaces

An extension can also implement any subset of these — the OSS server type-asserts at startup:

| Interface | When it fires | Failure semantics |
|---|---|---|
| `MiddlewareProvider` | Wraps every OSS route before registration | n/a |
| `AuthHook.OnSignup(ctx, userID, teamID, email)` | After team provisioning on email-activate or OAuth-new-signup | Error aborts signup with 500 `signup_hook_failed` (billing customer creation must succeed) |
| `AuthHook.OnLogin(ctx, userID)` | After a successful login or OAuth callback | Error logged, login still succeeds |
| `AuthHook.OnAccountSoftDelete(ctx, userID)` | After `DELETE /v1/me` commits | Error logged, request still succeeds |
| `AuthHook.OnAccountHardDelete(ctx, userID)` | After the 15-day cleanup goroutine purges a soft-deleted account | Error logged, cleanup continues |
| `SandboxEventHook.OnSandboxEvent(ctx, ev)` | Capsule create/pause/resume/destroy success, from the Redis stream consumer | Error leaves the message un-acked — hooks **must** be idempotent |
| `LimitsProvider.EffectiveLimits(ctx, teamID)` | `POST /v1/capsules` consults before scheduling | Returns 402 (`concurrent_sandbox_limit` / `vcpu_limit` / `memory_limit`) when over |
| `UsageProvider.CurrentUsage(ctx, teamID)` | Feeds `LimitsProvider` checks; falls back to OSS DB-backed default | Error → 402 `usage_unavailable` |

### Auth middleware helpers

For extensions that gate their own routes:

```go
r.With(cpextension.RequireSession(sctx)).Get("/billing", handler)
r.With(cpextension.RequireSessionOrAPIKey(sctx)).Get("/usage", handler)
r.With(cpextension.RequireSession(sctx), cpextension.RequireAdmin(sctx)).Get("/admin/exports", handler)

// Issue a session from a custom flow (e.g. invite-accept):
sess, err := cpextension.IssueSession(w, r, sctx, userID, teamID)
```

Cookie/header names are exported as `cpextension.SessionCookieName`, `CSRFCookieName`, `CSRFHeaderName`.

See `CLAUDE.md` for full architecture documentation.
