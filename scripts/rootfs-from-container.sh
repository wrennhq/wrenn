#!/usr/bin/env bash
#
# rootfs-from-container.sh — Create a bootable Wrenn rootfs from a Docker container.
#
# Exports a container's filesystem, writes it into an ext4 image, injects
# envd + wrenn-init, and shrinks the image to minimum size.
#
# Usage:
#   bash scripts/rootfs-from-container.sh <container> <image_name>
#
# Arguments:
#   container   — Docker container name or ID to export
#   image_name  — Directory name under images dir (e.g. "waitlist")
#
# Output:
#   ${AGENT_FILES_ROOTDIR}/images/<image_name>/rootfs.ext4
#
# Requires: docker, mkfs.ext4, resize2fs, e2fsck, make (for building envd), curl (for tini/socat download),
#           gcc, make (for building socat from source)
# Sudo is used only for mount/umount/copy-into-image operations.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
AGENT_FILES_ROOTDIR="${AGENT_FILES_ROOTDIR:-/var/lib/wrenn}"
AGENT_IMAGES_PATH="${AGENT_FILES_ROOTDIR}/images"

if [ $# -lt 2 ]; then
    echo "Usage: $0 <container> <image_name>"
    exit 1
fi

CONTAINER="$1"
IMAGE_NAME="$2"
OUTPUT_DIR="${AGENT_IMAGES_PATH}/${IMAGE_NAME}"
OUTPUT_FILE="${OUTPUT_DIR}/rootfs.ext4"
MOUNT_DIR="/tmp/wrenn-rootfs-build"
TAR_FILE="/tmp/wrenn-rootfs-export-${IMAGE_NAME}.tar"

# Verify the container exists.
if ! docker inspect "${CONTAINER}" > /dev/null 2>&1; then
    echo "ERROR: Container '${CONTAINER}' not found"
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

if ! file "${ENVD_BIN}" | grep -q "statically linked"; then
    echo "ERROR: envd is not statically linked!"
    exit 1
fi

# Step 2: Export container filesystem.
echo "==> Exporting container '${CONTAINER}'..."
docker export "${CONTAINER}" -o "${TAR_FILE}"

cleanup() {
    echo "==> Cleaning up..."
    sudo umount "${MOUNT_DIR}" 2>/dev/null || true
    rmdir "${MOUNT_DIR}" 2>/dev/null || true
    rm -f "${TAR_FILE}"
}
trap cleanup EXIT

# Step 3: Create an oversized ext4 image.
# Use 2x the tar size + 256MB headroom for filesystem overhead and injected binaries.
TAR_SIZE_BYTES="$(stat --format=%s "${TAR_FILE}")"
INITIAL_SIZE_MB=$(( (TAR_SIZE_BYTES / 1024 / 1024) * 2 + 256 ))
echo "==> Creating ${INITIAL_SIZE_MB}MB ext4 image (will shrink after populating)..."
sudo mkdir -p "${OUTPUT_DIR}"
sudo dd if=/dev/zero of="${OUTPUT_FILE}" bs=1M count="${INITIAL_SIZE_MB}" status=progress
sudo mkfs.ext4 -F "${OUTPUT_FILE}"

# Step 4: Mount and populate.
echo "==> Mounting image at ${MOUNT_DIR}..."
mkdir -p "${MOUNT_DIR}"
sudo mount -o loop "${OUTPUT_FILE}" "${MOUNT_DIR}"

echo "==> Extracting container filesystem..."
sudo tar xf "${TAR_FILE}" -C "${MOUNT_DIR}"

# Step 5: Inject wrenn guest binaries.
echo "==> Installing envd..."
sudo mkdir -p "${MOUNT_DIR}/usr/local/bin"
sudo cp "${ENVD_BIN}" "${MOUNT_DIR}/usr/local/bin/envd"
sudo chmod 755 "${MOUNT_DIR}/usr/local/bin/envd"

echo "==> Installing wrenn-init..."
sudo cp "${PROJECT_ROOT}/images/wrenn-init.sh" "${MOUNT_DIR}/usr/local/bin/wrenn-init"
sudo chmod 755 "${MOUNT_DIR}/usr/local/bin/wrenn-init"

echo "==> Installing tini..."
TINI_BIN=""
# 1. Already in the exported container image?
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

echo "==> Installing socat..."
SOCAT_BIN=""
# 1. Already in the exported container image?
for p in "${MOUNT_DIR}/usr/bin/socat" "${MOUNT_DIR}/usr/local/bin/socat"; do
    if [ -f "$p" ]; then SOCAT_BIN="$p"; break; fi
done
# 2. Available on the host?
if [ -z "${SOCAT_BIN}" ]; then
    for p in /usr/bin/socat /usr/local/bin/socat; do
        if [ -f "$p" ]; then SOCAT_BIN="$p"; break; fi
    done
fi
# 3. Build from source.
if [ -z "${SOCAT_BIN}" ]; then
    SOCAT_VERSION="1.8.1.1"
    SOCAT_URL="http://www.dest-unreach.org/socat/download/socat-${SOCAT_VERSION}.tar.gz"
    SOCAT_BUILD_DIR="/tmp/socat-build"
    echo "    Building socat ${SOCAT_VERSION} from source..."
    rm -rf "${SOCAT_BUILD_DIR}"
    mkdir -p "${SOCAT_BUILD_DIR}"
    curl -fsSL "${SOCAT_URL}" | tar xz -C "${SOCAT_BUILD_DIR}" --strip-components=1
    (cd "${SOCAT_BUILD_DIR}" && LDFLAGS="-static" ./configure --quiet && make -j"$(nproc)" -s)
    SOCAT_BIN="${SOCAT_BUILD_DIR}/socat"
    if [ ! -f "${SOCAT_BIN}" ]; then
        echo "ERROR: socat build failed"
        exit 1
    fi
    if ! file "${SOCAT_BIN}" | grep -q "statically linked"; then
        echo "ERROR: socat is not statically linked!"
        exit 1
    fi
fi
sudo cp "${SOCAT_BIN}" "${MOUNT_DIR}/usr/local/bin/socat"
sudo chmod 755 "${MOUNT_DIR}/usr/local/bin/socat"

# Step 7: Verify.
echo ""
echo "==> Installed guest binaries:"
ls -la "${MOUNT_DIR}/usr/local/bin/envd" "${MOUNT_DIR}/usr/local/bin/wrenn-init" "${MOUNT_DIR}/sbin/tini" "${MOUNT_DIR}/usr/local/bin/socat"

# Unmount before shrinking.
sudo umount "${MOUNT_DIR}"
rmdir "${MOUNT_DIR}" 2>/dev/null || true

# Step 8: Shrink the image to minimum size.
echo ""
echo "==> Shrinking image..."
sudo e2fsck -fy "${OUTPUT_FILE}"
sudo resize2fs -M "${OUTPUT_FILE}"

# Truncate the file to match the shrunk filesystem.
BLOCK_COUNT="$(sudo dumpe2fs -h "${OUTPUT_FILE}" 2>/dev/null | grep "Block count:" | awk '{print $3}')"
BLOCK_SIZE="$(sudo dumpe2fs -h "${OUTPUT_FILE}" 2>/dev/null | grep "Block size:" | awk '{print $3}')"
FS_SIZE_BYTES=$((BLOCK_COUNT * BLOCK_SIZE))
sudo truncate -s "${FS_SIZE_BYTES}" "${OUTPUT_FILE}"

FINAL_SIZE_MB=$((FS_SIZE_BYTES / 1024 / 1024))
echo ""
echo "==> Done. Rootfs created at: ${OUTPUT_FILE} (${FINAL_SIZE_MB}MB)"
