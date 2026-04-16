# Wrenn

Secure infrastructure for AI

## Prerequisites

- Linux host with `/dev/kvm` access (bare metal or nested virt)
- Firecracker binary at `/usr/local/bin/firecracker`
- PostgreSQL
- Go 1.25+
- pnpm (for frontend)
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

1. **Create a host record** (via API or dashboard):
   ```bash
   curl -X POST http://localhost:8000/v1/hosts \
     -H "Authorization: Bearer $JWT_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"type": "regular"}'
   ```
   This returns a `registration_token` (valid for 1 hour).

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

4. **If registration fails** (e.g., network error after token was consumed), regenerate a token:
   ```bash
   curl -X POST http://localhost:8000/v1/hosts/$HOST_ID/token \
     -H "Authorization: Bearer $JWT_TOKEN"
   ```
   Then restart the agent with the new token.

The agent sends heartbeats to the control plane every 30 seconds.

See `CLAUDE.md` for full architecture documentation.
