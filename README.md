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

The host agent needs a kernel, a minimal rootfs image, and working directories on the host machine.

### Directory structure

```
/var/lib/wrenn/
├── kernels/
│   └── vmlinux              # uncompressed Linux kernel (not bzImage)
├── images/
│   └── minimal/
│       └── rootfs.ext4      # base rootfs (all other templates snapshot from this)
├── sandboxes/               # per-sandbox CoW files (created at runtime)
└── snapshots/               # pause/hibernate snapshot files (created at runtime)
```

Create the directories:

```bash
sudo mkdir -p /var/lib/wrenn/{kernels,images/minimal,sandboxes,snapshots}
```

### Kernel

Place an uncompressed `vmlinux` kernel at `/var/lib/wrenn/kernels/vmlinux`. Versioned kernels (`vmlinux-{semver}`) are also supported — the agent picks the latest by semver.

### Minimal rootfs

The minimal rootfs is the base image that all other templates (Python, Node, etc.) are built on top of via device-mapper snapshots. It must contain:

| Package | Why |
|---------|-----|
| `socat` | Bidirectional relay for port forwarding |
| `chrony` | Time sync from KVM PTP clock (`/dev/ptp0`) |
| `tini` | PID 1 zombie reaper (injected by build script, not apt) |
| `sudo` | User privilege management inside the guest |
| `wget` | HTTP fetching |
| `curl` | HTTP client |
| `ca-certificates` | TLS certificate verification |

**To build a rootfs from a Docker container:**

1. Create and configure a container with the required packages:
   ```bash
   docker run -it --name wrenn-minimal debian:bookworm bash
   # Inside the container:
   apt update && apt install -y socat chrony sudo wget curl ca-certificates
   exit
   ```

2. Export to a rootfs image (builds envd, injects wrenn-init + tini, shrinks to minimum size):
   ```bash
   sudo bash scripts/rootfs-from-container.sh wrenn-minimal minimal
   ```

**To update an existing rootfs** after changing envd or `wrenn-init.sh`:

```bash
bash scripts/update-minimal-rootfs.sh
```

This rebuilds envd via `make build-envd` and copies the fresh binaries into the mounted rootfs image.

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

See `CLAUDE.md` for full architecture documentation.
