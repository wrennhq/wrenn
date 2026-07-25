#!/usr/bin/env bash
#
# build-ubuntu.sh — Build the minimal-ubuntu system base rootfs (template id 0).
#
# Usage: bash images/build-ubuntu.sh

set -euo pipefail
source "$(cd "$(dirname "$0")" && pwd)/build-common.sh"

PREP="set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update
# --no-install-recommends keeps the image lean (avoids pulling systemd-adjacent
# recommends). The guest never runs systemd: PID 1 is wrenn-init -> tini -> envd.
apt-get install -y --no-install-recommends socat chrony sudo wget curl ca-certificates git iproute2 hostname tini make nano vim e2fsprogs
# Remove the stock 'ubuntu' user (uid 1000) shipped by the base image; it is
# replaced by wrenn-user. Also drop its cloud-init sudoers drop-in.
userdel -r ubuntu 2>/dev/null || true
rm -f /etc/sudoers.d/90-cloud-init-users
useradd -m -s /bin/bash wrenn-user
${WRENN_SUDOERS_SETUP}
apt-get clean
rm -rf /var/lib/apt/lists/*"

build_system_rootfs "ubuntu:26.04" 0 "${PREP}"
