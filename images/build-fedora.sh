#!/usr/bin/env bash
#
# build-fedora.sh — Build the minimal-fedora system base rootfs (template id 3).
#
# Usage: bash images/build-fedora.sh

set -euo pipefail
source "$(cd "$(dirname "$0")" && pwd)/build-common.sh"

# Fedora's iproute package provides `ip` (no "2" suffix, unlike Debian/Arch).
PREP="set -e
# install_weak_deps=False keeps the image lean. The guest never runs systemd:
# PID 1 is wrenn-init -> tini -> envd.
dnf install -y --setopt=install_weak_deps=False socat chrony sudo wget curl ca-certificates git iproute hostname tini make nano vim e2fsprogs
useradd -m -s /bin/bash wrenn-user
${WRENN_SUDOERS_SETUP}
dnf clean all"

build_system_rootfs "fedora:45" 3 "${PREP}"
