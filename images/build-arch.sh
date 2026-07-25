#!/usr/bin/env bash
#
# build-arch.sh — Build the minimal-arch system base rootfs (template id 2).
#
# Arch is rolling-release; archlinux:base is the minimal base group.
#
# Usage: bash images/build-arch.sh

set -euo pipefail
source "$(cd "$(dirname "$0")" && pwd)/build-common.sh"

# tini is AUR-only on Arch (not in core/extra), so it is not installed here —
# rootfs-from-container.sh injects the static tini binary instead.
PREP="set -e
pacman -Sy --noconfirm --needed socat chrony sudo wget curl ca-certificates git iproute2 inetutils make nano vim e2fsprogs
useradd -m -s /bin/bash wrenn-user
${WRENN_SUDOERS_SETUP}
pacman -Scc --noconfirm || true"

build_system_rootfs "archlinux:base" 2 "${PREP}"
