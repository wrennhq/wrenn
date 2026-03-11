# Wrenn Sandbox

MicroVM-based code execution platform. Firecracker VMs, not containers. Pool-based pricing, persistent sandboxes, Python/TS/Go SDKs.

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

# Enable IP forwarding
sysctl -w net.ipv4.ip_forward=1
```

### Configure

Copy `.env.example` to `.env` and edit:

```bash
# Required
DATABASE_URL=postgres://wrenn:wrenn@localhost:5432/wrenn?sslmode=disable

# Control plane
CP_LISTEN_ADDR=:8000
CP_HOST_AGENT_ADDR=http://localhost:50051

# Host agent
AGENT_LISTEN_ADDR=:50051
AGENT_KERNEL_PATH=/var/lib/wrenn/kernels/vmlinux
AGENT_IMAGES_PATH=/var/lib/wrenn/images
AGENT_SANDBOXES_PATH=/var/lib/wrenn/sandboxes
```

### Run

```bash
# Apply database migrations
make migrate-up

# Start host agent (requires root)
sudo ./builds/wrenn-agent

# Start control plane
./builds/wrenn-cp
```

Control plane listens on `CP_LISTEN_ADDR` (default `:8000`). Host agent listens on `AGENT_LISTEN_ADDR` (default `:50051`).

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
