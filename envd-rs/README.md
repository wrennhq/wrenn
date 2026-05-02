# envd (Rust)

Wrenn guest agent daemon — runs as PID 1 inside Firecracker microVMs. Provides process management, filesystem operations, file transfer, port forwarding, and VM lifecycle control over Connect RPC and HTTP.

Rust rewrite of `envd/` (Go). Drop-in replacement — same wire protocol, same endpoints, same CLI flags.

## Prerequisites

- Rust 1.88+ (required by `connectrpc` 0.3.3)
- `protoc` (protobuf compiler, for proto codegen at build time)
- `musl-tools` (for static linking)

```bash
# Ubuntu/Debian
sudo apt install musl-tools protobuf-compiler

# Rust musl target
rustup target add x86_64-unknown-linux-musl
```

## Building

### Static binary (production — what goes into the rootfs)

```bash
cd envd-rs
ENVD_COMMIT=$(git rev-parse --short HEAD) \
  cargo build --release --target x86_64-unknown-linux-musl
```

Output: `target/x86_64-unknown-linux-musl/release/envd`

Verify static linking:

```bash
file target/x86_64-unknown-linux-musl/release/envd
# should say: "statically linked"

ldd target/x86_64-unknown-linux-musl/release/envd
# should say: "not a dynamic executable"
```

### Debug binary (dev machine, dynamically linked)

```bash
cd envd-rs
cargo build
```

Run locally (outside a VM):

```bash
./target/debug/envd --isnotfc --port 49983
```

### Via Makefile (from repo root)

```bash
make build-envd        # static musl release build
make build-envd-go     # Go version (for comparison)
```

## CLI Flags

```
--port <PORT>          Listen port [default: 49983]
--isnotfc              Not running inside Firecracker (disables MMDS, cgroups)
--version              Print version and exit
--commit               Print git commit and exit
--cmd <CMD>            Spawn a process at startup (e.g. --cmd "/bin/bash")
--cgroup-root <PATH>   Cgroup v2 root [default: /sys/fs/cgroup]
```

## Endpoints

### HTTP

| Method | Path                | Description                          |
|--------|---------------------|--------------------------------------|
| GET    | `/health`           | Health check, triggers post-restore  |
| GET    | `/metrics`          | System metrics (CPU, memory, disk)   |
| GET    | `/envs`             | Current environment variables        |
| POST   | `/init`             | Host agent init (token, env, mounts) |
| POST   | `/snapshot/prepare` | Quiesce before Firecracker snapshot  |
| GET    | `/files`            | Download file (gzip, range support)  |
| POST   | `/files`            | Upload file(s) via multipart         |

### Connect RPC (same port)

| Service    | RPCs                                                                    |
|------------|-------------------------------------------------------------------------|
| Process    | List, Start, Connect, Update, StreamInput, SendInput, SendSignal, CloseStdin |
| Filesystem | Stat, MakeDir, Move, ListDir, Remove, WatchDir, CreateWatcher, GetWatcherEvents, RemoveWatcher |

## Architecture

```
42 files, ~4200 LOC Rust
Binary: ~4 MB (stripped, LTO, musl static)

src/
├── main.rs              # Entry point, CLI, server setup
├── state.rs             # Shared AppState
├── config.rs            # Constants
├── conntracker.rs       # TCP connection tracking for snapshot/restore
├── execcontext.rs       # Default user/workdir/env
├── logging.rs           # tracing-subscriber (JSON or pretty)
├── util.rs              # AtomicMax
├── auth/                # Token, signing, middleware
├── crypto/              # SHA-256, SHA-512, HMAC
├── host/                # MMDS polling, system metrics
├── http/                # Axum handlers (health, init, snapshot, files, encoding)
├── permissions/         # Path resolution, user lookup, chown
├── rpc/                 # Connect RPC services
│   ├── pb.rs            # Generated proto types
│   ├── process_*.rs     # Process service + handler (PTY, pipe, broadcast)
│   ├── filesystem_*.rs  # Filesystem service (stat, list, watch, mkdir, move, remove)
│   └── entry.rs         # EntryInfo builder
├── port/                # Port subsystem
│   ├── conn.rs          # /proc/net/tcp parser
│   ├── scanner.rs       # Periodic TCP port scanner
│   ├── forwarder.rs     # socat-based port forwarding
│   └── subsystem.rs     # Lifecycle (start/stop/restart)
└── cgroups/             # Cgroup v2 manager (pty/user/socat groups)
```

## Updating the rootfs

After building the static binary, copy it into the rootfs:

```bash
bash scripts/update-debug-rootfs.sh [rootfs_path]
```

Or manually:

```bash
sudo mount -o loop /var/lib/wrenn/images/minimal.ext4 /mnt
sudo cp target/x86_64-unknown-linux-musl/release/envd /mnt/usr/bin/envd
sudo umount /mnt
```
