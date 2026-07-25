#!/usr/bin/env bash
#
# build-alpine.sh — Build the minimal-alpine system base rootfs (template id 1).
#
# Usage: bash images/build-alpine.sh

set -euo pipefail
source "$(cd "$(dirname "$0")" && pwd)/build-common.sh"

# Alpine is musl-based: the static envd + static tini run fine. bash is added so
# wrenn-user has a familiar login shell; wrenn-init itself only needs /bin/sh.
PREP="set -e
apk add --no-cache socat chrony sudo wget curl ca-certificates git iproute2 tini bash make nano vim e2fsprogs util-linux
adduser -D wrenn-user
${WRENN_SUDOERS_SETUP}"

build_system_rootfs "alpine:3.22" 1 "${PREP}"
