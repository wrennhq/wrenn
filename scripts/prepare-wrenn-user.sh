#!/usr/bin/env bash
#
# prepare-wrenn-user.sh — Create the wrenn system user and configure minimal privileges.
#
# Creates a locked-down 'wrenn' system user that can run wrenn-agent and wrenn-cp
# with only the privileges they need. The agent binary gets Linux capabilities
# via setcap — no sudo is configured for the wrenn user at all. If an attacker
# compromises the wrenn user, they cannot escalate via sudo.
#
# What this script does:
#   1. Creates the 'wrenn' system user (bash shell for debugging, no home dir)
#   2. Creates required directories with correct ownership
#   3. Sets Linux capabilities on wrenn-agent and all child binaries
#   4. Installs an apt hook to restore capabilities after package updates
#   5. Installs a sudoers drop-in (comment-only, no grants — absence is the cage)
#   6. Ensures required kernel modules are loaded
#   7. Writes systemd unit files for both wrenn-agent and wrenn-cp
#
# Usage:
#   sudo bash scripts/prepare-wrenn-user.sh
#
# Prerequisites:
#   - wrenn-agent binary at /usr/local/bin/wrenn-agent
#   - wrenn-cp binary at /usr/local/bin/wrenn-cp
#   - firecracker binary at /usr/local/bin/firecracker
#   - libcap2-bin installed (for setcap)

set -euo pipefail

# ── Guard ────────────────────────────────────────────────────────────────────

if [[ $EUID -ne 0 ]]; then
    echo "ERROR: This script must be run as root."
    exit 1
fi

# ── Configuration ────────────────────────────────────────────────────────────

WRENN_USER="wrenn"
WRENN_GROUP="wrenn"
WRENN_DIR="/var/lib/wrenn"
AGENT_BIN="/usr/local/bin/wrenn-agent"
CP_BIN="/usr/local/bin/wrenn-cp"
FC_BIN="/usr/local/bin/firecracker"
RESTORE_CAPS_SCRIPT="/etc/wrenn/restore-caps.sh"

# ── 1. Create system user ───────────────────────────────────────────────────

if id "${WRENN_USER}" &>/dev/null; then
    echo "==> User '${WRENN_USER}' already exists, skipping creation."
else
    echo "==> Creating system user '${WRENN_USER}'..."
    useradd \
        --system \
        --no-create-home \
        --home-dir "${WRENN_DIR}" \
        --shell /bin/bash \
        "${WRENN_USER}"
fi

# Add wrenn to kvm group for /dev/kvm access.
if getent group kvm &>/dev/null; then
    usermod -aG kvm "${WRENN_USER}"
    echo "==> Added '${WRENN_USER}' to 'kvm' group."
fi

# ── 2. Create directories with correct ownership ────────────────────────────

echo "==> Setting up directories..."

directories=(
    "${WRENN_DIR}"
    "${WRENN_DIR}/images"
    "${WRENN_DIR}/kernels"
    "${WRENN_DIR}/sandboxes"
    "${WRENN_DIR}/snapshots"
    "${WRENN_DIR}/logs"
    "/run/netns"
)

for dir in "${directories[@]}"; do
    mkdir -p "${dir}"
done

# Only chown wrenn-owned dirs (not /run/netns which is system-managed).
for dir in "${WRENN_DIR}" "${WRENN_DIR}/images" "${WRENN_DIR}/kernels" \
           "${WRENN_DIR}/sandboxes" "${WRENN_DIR}/snapshots" "${WRENN_DIR}/logs"; do
    chown "${WRENN_USER}:${WRENN_GROUP}" "${dir}"
    chmod 750 "${dir}"
done

# ── 3. Set capabilities on binaries ─────────────────────────────────────────
#
# These capabilities replace full root access. The wrenn-agent binary gets
# exactly the capabilities it needs for:
#
#   CAP_SYS_ADMIN   — network namespaces (netns create/enter), mount namespaces
#                     (unshare -m), losetup, dmsetup, mount/umount
#   CAP_NET_ADMIN   — veth/TAP creation (netlink), iptables rules, IP forwarding,
#                     routing table manipulation
#   CAP_NET_RAW     — raw socket access (needed by iptables internally)
#   CAP_SYS_PTRACE  — reading /proc/self/ns/net (netns.Get)
#   CAP_KILL        — sending SIGTERM/SIGKILL to Firecracker processes
#   CAP_DAC_OVERRIDE — accessing /dev/loop*, /dev/mapper/*, /dev/net/tun,
#                      /proc/sys/net/ipv4/ip_forward
#   CAP_MKNOD       — creating device nodes (dm-snapshot)
#
# The 'ep' suffix means Effective + Permitted (granted at exec time).

echo "==> Setting capabilities on wrenn-agent..."

if [[ ! -f "${AGENT_BIN}" ]]; then
    echo "WARNING: ${AGENT_BIN} not found, skipping setcap. Install the binary first."
else
    setcap \
        cap_sys_admin,cap_net_admin,cap_net_raw,cap_sys_ptrace,cap_kill,cap_dac_override,cap_mknod+ep \
        "${AGENT_BIN}"

    echo "    Capabilities set on ${AGENT_BIN}:"
    getcap "${AGENT_BIN}"
fi

# Firecracker also needs capabilities when spawned by a non-root parent.
# CAP_NET_ADMIN is required for network device access inside the netns.
if [[ -f "${FC_BIN}" ]]; then
    setcap cap_net_admin,cap_sys_admin,cap_dac_override+ep "${FC_BIN}"
    echo "    Capabilities set on ${FC_BIN}:"
    getcap "${FC_BIN}"
fi

# ── Helper: resolve binary path and apply setcap ────────────────────────────
#
# Uses `command -v` to find the binary in PATH (handles /usr/bin vs /usr/sbin
# differences across distros), then `readlink -f` to resolve symlinks so that
# setcap hits the real inode (important for iptables-nft/alternatives).

setcap_binary() {
    local name="$1" caps="$2"
    local bin
    bin=$(command -v "$name" 2>/dev/null) || {
        echo "    WARNING: ${name} not found in PATH, skipping."
        return 0
    }
    bin=$(readlink -f "$bin")
    setcap "$caps" "$bin"
    echo "    $(getcap "$bin")"
}

# The child binaries invoked by wrenn-agent (iptables, losetup, dmsetup, etc.)
# also need capabilities since they'll be exec'd by a non-root user.
echo "==> Setting capabilities on child binaries..."

setcap_binary iptables      "cap_net_admin,cap_net_raw+ep"
setcap_binary iptables-save "cap_net_admin,cap_net_raw+ep"
setcap_binary ip            "cap_sys_admin,cap_net_admin+ep"
setcap_binary sysctl        "cap_net_admin+ep"
setcap_binary losetup       "cap_sys_admin,cap_dac_override+ep"
setcap_binary blockdev      "cap_sys_admin,cap_dac_override+ep"
setcap_binary dmsetup       "cap_sys_admin,cap_dac_override,cap_mknod+ep"
setcap_binary e2fsck        "cap_sys_admin,cap_dac_override+ep"
setcap_binary resize2fs     "cap_sys_admin,cap_dac_override+ep"
setcap_binary dd            "cap_dac_override+ep"
setcap_binary unshare       "cap_sys_admin+ep"
setcap_binary mount         "cap_sys_admin,cap_dac_override+ep"

# ── 4. Persist capabilities across package updates ──────────────────────────
#
# apt/dpkg overwrites binaries on package updates, which strips the xattr-based
# capabilities set by setcap. This installs:
#   - /etc/wrenn/restore-caps.sh: re-applies setcap to all child binaries
#   - /etc/apt/apt.conf.d/99-wrenn-setcap: apt post-invoke hook that calls it

echo "==> Installing capability restore hook..."

mkdir -p /etc/wrenn

cat > "${RESTORE_CAPS_SCRIPT}" << 'RESTORE'
#!/usr/bin/env bash
#
# restore-caps.sh — Re-apply Linux capabilities to wrenn child binaries.
# Called automatically by apt after package updates (see /etc/apt/apt.conf.d/99-wrenn-setcap).
# Can also be run manually: sudo /etc/wrenn/restore-caps.sh

set -euo pipefail

setcap_binary() {
    local name="$1" caps="$2"
    local bin
    bin=$(command -v "$name" 2>/dev/null) || return 0
    bin=$(readlink -f "$bin")
    setcap "$caps" "$bin" 2>/dev/null || true
}

# wrenn-agent and firecracker (only if present — they aren't package-managed).
[[ -f /usr/local/bin/wrenn-agent ]] && \
    setcap cap_sys_admin,cap_net_admin,cap_net_raw,cap_sys_ptrace,cap_kill,cap_dac_override,cap_mknod+ep \
        /usr/local/bin/wrenn-agent 2>/dev/null || true
[[ -f /usr/local/bin/firecracker ]] && \
    setcap cap_net_admin,cap_sys_admin,cap_dac_override+ep \
        /usr/local/bin/firecracker 2>/dev/null || true

# Child binaries (these are the ones wiped by apt).
setcap_binary iptables      "cap_net_admin,cap_net_raw+ep"
setcap_binary iptables-save "cap_net_admin,cap_net_raw+ep"
setcap_binary ip            "cap_sys_admin,cap_net_admin+ep"
setcap_binary sysctl        "cap_net_admin+ep"
setcap_binary losetup       "cap_sys_admin,cap_dac_override+ep"
setcap_binary blockdev      "cap_sys_admin,cap_dac_override+ep"
setcap_binary dmsetup       "cap_sys_admin,cap_dac_override,cap_mknod+ep"
setcap_binary e2fsck        "cap_sys_admin,cap_dac_override+ep"
setcap_binary resize2fs     "cap_sys_admin,cap_dac_override+ep"
setcap_binary dd            "cap_dac_override+ep"
setcap_binary unshare       "cap_sys_admin+ep"
setcap_binary mount         "cap_sys_admin,cap_dac_override+ep"
RESTORE

chmod 755 "${RESTORE_CAPS_SCRIPT}"

cat > /etc/apt/apt.conf.d/99-wrenn-setcap << 'APT'
// Re-apply Linux capabilities to wrenn child binaries after any package update.
// Capabilities (xattr) are stripped when dpkg overwrites a binary.
DPkg::Post-Invoke { "/etc/wrenn/restore-caps.sh"; };
APT

echo "    Installed ${RESTORE_CAPS_SCRIPT} and apt post-invoke hook."

# ── 5. Device access ────────────────────────────────────────────────────────
#
# /dev/kvm   — handled by kvm group membership above
# /dev/net/tun — needs to be accessible by wrenn user

echo "==> Configuring device access..."

# Ensure /dev/net/tun is accessible (udev rule for persistence across reboots).
cat > /etc/udev/rules.d/99-wrenn.rules << 'UDEV'
# Allow wrenn user access to TUN device for TAP networking.
SUBSYSTEM=="misc", KERNEL=="tun", GROUP="wrenn", MODE="0660"
UDEV

udevadm control --reload-rules 2>/dev/null || true
echo "    Installed udev rule for /dev/net/tun."

# ── 6. Kernel modules ───────────────────────────────────────────────────────

echo "==> Ensuring kernel modules are loaded..."

modules=(dm_snapshot dm_mod loop tun)
for mod in "${modules[@]}"; do
    if ! lsmod | grep -q "^${mod}"; then
        modprobe "${mod}" 2>/dev/null && echo "    Loaded ${mod}" || echo "    WARNING: Could not load ${mod}"
    else
        echo "    ${mod} already loaded."
    fi
done

# Persist across reboots.
for mod in "${modules[@]}"; do
    grep -qxF "${mod}" /etc/modules-load.d/wrenn.conf 2>/dev/null || echo "${mod}" >> /etc/modules-load.d/wrenn.conf
done
echo "    Module persistence written to /etc/modules-load.d/wrenn.conf."

# ── 7. Sudoers ──────────────────────────────────────────────────────────────
#
# The wrenn user has no sudo grants. The absence of a grant is the cage — an
# explicit "!ALL" deny is weaker due to known bypasses (CVE-2019-14287).
# This file exists purely as documentation for operators running `sudo -l`.

echo "==> Writing sudoers drop-in..."

cat > /etc/sudoers.d/wrenn << 'SUDOERS'
# Wrenn system user — no sudo access permitted.
# All privilege is granted via Linux capabilities on specific binaries (setcap).
# This file contains no active rules. The absence of any grant is intentional
# and is the strongest way to deny escalation.
#
# Do not add rules here. If the wrenn user needs new privileges, use setcap
# on the specific binary instead.
SUDOERS

chmod 440 /etc/sudoers.d/wrenn
visudo -c -f /etc/sudoers.d/wrenn
echo "    /etc/sudoers.d/wrenn installed and validated."

# ── 8. Systemd units ────────────────────────────────────────────────────────

echo "==> Writing systemd service files..."

cat > /etc/systemd/system/wrenn-agent.service << 'UNIT'
[Unit]
Description=Wrenn Host Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=wrenn
Group=wrenn
EnvironmentFile=-/etc/wrenn/agent.env

# The binary has capabilities set via setcap. These systemd directives ensure
# the capabilities are inherited into the process at exec time.
AmbientCapabilities=CAP_SYS_ADMIN CAP_NET_ADMIN CAP_NET_RAW CAP_SYS_PTRACE CAP_KILL CAP_DAC_OVERRIDE CAP_MKNOD
CapabilityBoundingSet=CAP_SYS_ADMIN CAP_NET_ADMIN CAP_NET_RAW CAP_SYS_PTRACE CAP_KILL CAP_DAC_OVERRIDE CAP_MKNOD

# IMPORTANT: must be false — child binaries (iptables, losetup, dmsetup, etc.)
# have their own file capabilities via setcap which must be honored at exec time.
NoNewPrivileges=false

# Enable IP forwarding before the agent starts. The "+" prefix runs this
# directive as root (bypassing User=wrenn) so it can write to procfs.
ExecStartPre=+/bin/sh -c 'sysctl -w net.ipv4.ip_forward=1'

ExecStart=/usr/local/bin/wrenn-agent --address ${WRENN_ADVERTISE_ADDR}

Restart=on-failure
RestartSec=5

# File descriptor limits (Firecracker + loop devices + sockets).
LimitNOFILE=65536
LimitNPROC=4096

# Protect host filesystem — only allow access to what's needed.
ProtectHome=true
ReadWritePaths=/var/lib/wrenn /tmp /run/netns /dev/mapper
ReadOnlyPaths=/usr/local/bin/firecracker

[Install]
WantedBy=multi-user.target
UNIT

cat > /etc/systemd/system/wrenn-cp.service << 'UNIT'
[Unit]
Description=Wrenn Control Plane
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=wrenn
Group=wrenn
EnvironmentFile=-/etc/wrenn/cp.env

# Control plane is fully unprivileged — no capabilities needed.
NoNewPrivileges=true
CapabilityBoundingSet=

ExecStart=/usr/local/bin/wrenn-cp

Restart=on-failure
RestartSec=5

ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/tmp

[Install]
WantedBy=multi-user.target
UNIT

mkdir -p /etc/wrenn
touch /etc/wrenn/agent.env /etc/wrenn/cp.env
chmod 640 /etc/wrenn/agent.env /etc/wrenn/cp.env
chown root:${WRENN_GROUP} /etc/wrenn/agent.env /etc/wrenn/cp.env

systemctl daemon-reload
echo "    wrenn-agent.service and wrenn-cp.service installed."

# ── Done ─────────────────────────────────────────────────────────────────────

echo ""
echo "=== Setup complete ==="
echo ""
echo "Next steps:"
echo "  1. Copy wrenn-agent and wrenn-cp binaries to /usr/local/bin/"
echo "  2. Edit /etc/wrenn/agent.env with WRENN_CP_URL and WRENN_ADVERTISE_ADDR"
echo "  3. Edit /etc/wrenn/cp.env with DATABASE_URL and other control plane config"
echo "  4. systemctl enable --now wrenn-agent"
echo "  5. systemctl enable --now wrenn-cp"
echo ""
echo "Security summary:"
echo "  - wrenn user: bash shell (for debugging), no home, no sudo (no grants in sudoers)"
echo "  - wrenn-agent: runs as wrenn with 7 capabilities via setcap (not root)"
echo "  - wrenn-cp: runs as wrenn with zero capabilities"
echo "  - Capabilities auto-restored after apt upgrades via /etc/wrenn/restore-caps.sh"
echo ""
