#!/usr/bin/env bash
#
# update-debug-rootfs.sh — Build envd and inject it (plus wrenn-init) into the debug rootfs.
#
# This script:
#   1. Builds a fresh envd static binary via make
#   2. Mounts the rootfs image
#   3. Copies envd and wrenn-init into the image
#   4. Unmounts cleanly
#
# Usage:
#   bash scripts/update-debug-rootfs.sh [rootfs_path]
#
# Defaults to /var/lib/wrenn/images/minimal/rootfs.ext4

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ROOTFS="${1:-/var/lib/wrenn/images/minimal/rootfs.ext4}"
MOUNT_DIR="/tmp/wrenn-rootfs-update"

if [ ! -f "${ROOTFS}" ]; then
    echo "ERROR: Rootfs not found at ${ROOTFS}"
    exit 1
fi

# Step 1: Build envd.
echo "==> Building envd..."
cd "${PROJECT_ROOT}"
make build-envd
ENVD_BIN="${PROJECT_ROOT}/builds/envd"

if [ ! -f "${ENVD_BIN}" ]; then
    echo "ERROR: envd binary not found at ${ENVD_BIN}"
    exit 1
fi

# Verify it's statically linked.
if ! file "${ENVD_BIN}" | grep -q "statically linked"; then
    echo "ERROR: envd is not statically linked!"
    exit 1
fi

# Step 2: Mount the rootfs.
echo "==> Mounting rootfs at ${MOUNT_DIR}..."
mkdir -p "${MOUNT_DIR}"
sudo mount -o loop "${ROOTFS}" "${MOUNT_DIR}"

cleanup() {
    echo "==> Unmounting rootfs..."
    sudo umount "${MOUNT_DIR}" 2>/dev/null || true
    rmdir "${MOUNT_DIR}" 2>/dev/null || true
}
trap cleanup EXIT

# Step 3: Copy files into rootfs.
echo "==> Installing envd..."
sudo mkdir -p "${MOUNT_DIR}/usr/local/bin"
sudo cp "${ENVD_BIN}" "${MOUNT_DIR}/usr/local/bin/envd"
sudo chmod 755 "${MOUNT_DIR}/usr/local/bin/envd"

echo "==> Installing wrenn-init..."
sudo cp "${PROJECT_ROOT}/images/wrenn-init.sh" "${MOUNT_DIR}/usr/local/bin/wrenn-init"
sudo chmod 755 "${MOUNT_DIR}/usr/local/bin/wrenn-init"

echo "==> Installing tini..."
TINI_BIN=""
# 1. Already in the rootfs?
for p in "${MOUNT_DIR}/usr/bin/tini" "${MOUNT_DIR}/sbin/tini" "${MOUNT_DIR}/usr/local/bin/tini"; do
    if [ -f "$p" ]; then TINI_BIN="$p"; break; fi
done
# 2. Available on the host?
if [ -z "${TINI_BIN}" ]; then
    for p in /usr/bin/tini /usr/local/bin/tini /sbin/tini; do
        if [ -f "$p" ]; then TINI_BIN="$p"; break; fi
    done
fi
# 3. Download from GitHub releases.
if [ -z "${TINI_BIN}" ]; then
    ARCH="$(uname -m)"
    case "${ARCH}" in
        x86_64)  TINI_ARCH="amd64" ;;
        aarch64) TINI_ARCH="arm64" ;;
        *)        echo "ERROR: Unsupported architecture: ${ARCH}"; exit 1 ;;
    esac
    TINI_VERSION="v0.19.0"
    TINI_URL="https://github.com/krallin/tini/releases/download/${TINI_VERSION}/tini-${TINI_ARCH}"
    TINI_TMP="/tmp/tini-${TINI_ARCH}"
    echo "    Downloading tini ${TINI_VERSION} (${TINI_ARCH})..."
    curl -fsSL "${TINI_URL}" -o "${TINI_TMP}"
    chmod +x "${TINI_TMP}"
    TINI_BIN="${TINI_TMP}"
fi
sudo mkdir -p "${MOUNT_DIR}/sbin"
sudo cp "${TINI_BIN}" "${MOUNT_DIR}/sbin/tini"
sudo chmod 755 "${MOUNT_DIR}/sbin/tini"

# Step 4: Verify.
echo ""
echo "==> Installed files:"
ls -la "${MOUNT_DIR}/usr/local/bin/envd" "${MOUNT_DIR}/usr/local/bin/wrenn-init" "${MOUNT_DIR}/sbin/tini"

echo ""
echo "==> Done. Rootfs updated: ${ROOTFS}"
