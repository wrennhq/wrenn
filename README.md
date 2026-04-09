# Wrenn

Secure infrastructure for AI

## Deployment

### Prerequisites

- Linux host with `/dev/kvm` access (bare metal or nested virt)
- Firecracker binary at `/usr/local/bin/firecracker`
- PostgreSQL
- Go 1.25+

### Build

```bash
make build    # outputs to builds/
```

Produces three binaries: `wrenn-cp` (control plane), `wrenn-agent` (host agent), `envd` (guest agent).

### Host setup

The host agent machine needs:

```bash
# Kernel for guest VMs
mkdir -p /var/lib/wrenn/kernels
# Place a vmlinux kernel at /var/lib/wrenn/kernels/vmlinux

# Rootfs images
mkdir -p /var/lib/wrenn/images
# Build or place .ext4 rootfs images (e.g., minimal.ext4)

# Sandbox working directory
mkdir -p /var/lib/wrenn/sandboxes

# Snapshots directory
mkdir -p /var/lib/wrenn/snapshots

# Enable IP forwarding
sysctl -w net.ipv4.ip_forward=1
```

### Configure

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

### Run

```bash
# Apply database migrations
make migrate-up

# Start control plane
./builds/wrenn-cp
```

Control plane listens on `WRENN_CP_LISTEN_ADDR` (default `:8000`).

### Host registration

Hosts must be registered with the control plane before they can serve sandboxes.

1. **Create a host record** (via API or dashboard):
   ```bash
   # As an admin (JWT auth)
   curl -X POST http://localhost:8000/v1/hosts \
     -H "Authorization: Bearer $JWT_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"type": "regular"}'
   ```
   This returns a `registration_token` (valid for 1 hour).

2. **Start the host agent** with the registration token and its externally-reachable address:
   ```bash
   sudo WRENN_CP_URL=http://cp-host:8000 \
        ./builds/wrenn-agent \
        --register <token-from-step-1> \
        --address 10.0.1.5:50051
   ```
   On first startup the agent sends its specs (arch, CPU, memory, disk) to the control plane, receives a long-lived host JWT, and saves it to `$WRENN_DIR/host-token`.

3. **Subsequent startups** don't need `--register` — the agent loads the saved JWT automatically:
   ```bash
   sudo WRENN_CP_URL=http://cp-host:8000 \
        ./builds/wrenn-agent --address 10.0.1.5:50051
   ```

4. **If registration fails** (e.g., network error after token was consumed), regenerate a token:
   ```bash
   curl -X POST http://localhost:8000/v1/hosts/$HOST_ID/token \
     -H "Authorization: Bearer $JWT_TOKEN"
   ```
   Then restart the agent with the new token.

The agent sends heartbeats to the control plane every 30 seconds. Host agent listens on `WRENN_HOST_LISTEN_ADDR` (default `:50051`).

### Rootfs images

envd must be baked into every rootfs image. After building:

```bash
make build-envd
bash scripts/update-debug-rootfs.sh /var/lib/wrenn/images/minimal.ext4
```

## Development

```bash
make dev          # Start PostgreSQL (Docker), run migrations, start control plane
make dev-agent    # Start host agent (separate terminal, sudo)
make check        # fmt + vet + lint + test
```

See `CLAUDE.md` for full architecture documentation.
