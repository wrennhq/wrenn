#!/usr/bin/env bash
#
# update-minimal-rootfs.sh — Rebuild envd and inject it (plus wrenn-init + tini)
# into the system base rootfs images.
#
# This script:
#   1. Builds a fresh envd static binary via make (once)
#   2. For each system base rootfs (ubuntu/alpine/arch/fedora): mounts it,
#      copies envd + wrenn-init + tini in, and unmounts cleanly
#
# Usage:
#   bash scripts/update-minimal-rootfs.sh [rootfs_path]
#
# With no argument it updates all four system base rootfs images under
#   ${WRENN_DIR}/images/teams/<platform>/<id>/rootfs.ext4
# With a path argument it updates only that single rootfs.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
WRENN_DIR="${WRENN_DIR:-/var/lib/wrenn}"
MOUNT_DIR="/tmp/wrenn-rootfs-update"

# base36(all-zeros UUID) = platform team that owns every system base template.
PLATFORM_TEAM_B36="0000000000000000000000000"

# System base template IDs (well-known reserved IDs 0..3). Single-digit IDs, so
# the 25-char base36 string is just the zero-padded decimal.
SYSTEM_TEMPLATE_IDS=(0 1 2 3)

# Resolve which rootfs images to update.
ROOTFS_LIST=()
if [ $# -ge 1 ]; then
    ROOTFS_LIST=("$1")
else
    for tid in "${SYSTEM_TEMPLATE_IDS[@]}"; do
        tmpl_b36="$(printf '%025d' "${tid}")"
        ROOTFS_LIST+=("${WRENN_DIR}/images/teams/${PLATFORM_TEAM_B36}/${tmpl_b36}/rootfs.ext4")
    done
fi

# Step 1: Build envd (once).
echo "==> Building envd..."
cd "${PROJECT_ROOT}"
make build-envd
ENVD_BIN="${PROJECT_ROOT}/builds/envd"

if [ ! -f "${ENVD_BIN}" ]; then
    echo "ERROR: envd binary not found at ${ENVD_BIN}"
    exit 1
fi

if ! ldd "${ENVD_BIN}" | grep -q "statically linked"; then
    echo "ERROR: envd is not statically linked!"
    exit 1
fi

# resolve_tini ROOTFS_MOUNT — echo a path to a tini binary suitable for the
# mounted rootfs. Prefers one already in the image, then a static download.
resolve_tini() {
    local mount_dir="$1" p tini_arch arch
    for p in "${mount_dir}/usr/bin/tini" "${mount_dir}/sbin/tini" "${mount_dir}/usr/local/bin/tini"; do
        if [ -f "$p" ]; then echo "$p"; return; fi
    done
    arch="$(uname -m)"
    case "${arch}" in
        x86_64)  tini_arch="amd64" ;;
        aarch64) tini_arch="arm64" ;;
        *) echo "ERROR: Unsupported architecture: ${arch}" >&2; exit 1 ;;
    esac
    # Static tini runs under any libc (glibc or musl).
    local tmp="/tmp/tini-static-${tini_arch}"
    if [ ! -f "${tmp}" ]; then
        echo "    Downloading tini v0.19.0 static (${tini_arch})..." >&2
        curl -fsSL "https://github.com/krallin/tini/releases/download/v0.19.0/tini-static-${tini_arch}" -o "${tmp}"
        chmod +x "${tmp}"
    fi
    echo "${tmp}"
}

# inject_rootfs ROOTFS — mount, copy guest binaries in, unmount.
inject_rootfs() {
    local rootfs="$1" tini_bin
    echo ""
    echo "==> Updating ${rootfs}"

    mkdir -p "${MOUNT_DIR}"
    sudo mount -o loop,rw "${rootfs}" "${MOUNT_DIR}"

    local mounted=1
    cleanup_mount() {
        if [ "${mounted}" = "1" ]; then
            sudo umount "${MOUNT_DIR}" 2>/dev/null || true
            rmdir "${MOUNT_DIR}" 2>/dev/null || true
            mounted=0
        fi
    }
    trap cleanup_mount RETURN

    sudo mkdir -p "${MOUNT_DIR}/usr/local/bin"
    sudo cp "${ENVD_BIN}" "${MOUNT_DIR}/usr/local/bin/envd"
    sudo chmod 755 "${MOUNT_DIR}/usr/local/bin/envd"

    sudo cp "${PROJECT_ROOT}/images/wrenn-init.sh" "${MOUNT_DIR}/usr/local/bin/wrenn-init"
    sudo chmod 755 "${MOUNT_DIR}/usr/local/bin/wrenn-init"

    tini_bin="$(resolve_tini "${MOUNT_DIR}")"
    sudo mkdir -p "${MOUNT_DIR}/sbin"
    # On usr-merged distros (e.g. Fedora) /sbin -> /usr/bin, so a tini already at
    # /usr/bin/tini IS /sbin/tini — copying onto itself errors. Skip then.
    if [ "${tini_bin}" -ef "${MOUNT_DIR}/sbin/tini" ]; then
        echo "    tini already at /sbin/tini (usr-merged); skipping copy"
    else
        sudo cp "${tini_bin}" "${MOUNT_DIR}/sbin/tini"
    fi
    sudo chmod 755 "${MOUNT_DIR}/sbin/tini"

    ls -la "${MOUNT_DIR}/usr/local/bin/envd" "${MOUNT_DIR}/usr/local/bin/wrenn-init" "${MOUNT_DIR}/sbin/tini"
    cleanup_mount
}

# Step 2: Update each rootfs that exists.
UPDATED=0
for rootfs in "${ROOTFS_LIST[@]}"; do
    if [ ! -f "${rootfs}" ]; then
        echo "==> Skipping (not found): ${rootfs}"
        continue
    fi
    inject_rootfs "${rootfs}"
    UPDATED=$((UPDATED + 1))
done

echo ""
if [ "${UPDATED}" -eq 0 ]; then
    echo "==> No rootfs images updated. Build them first with: make images"
    exit 1
fi
echo "==> Done. Updated ${UPDATED} rootfs image(s)."
