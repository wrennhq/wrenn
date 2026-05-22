#!/usr/bin/env bash
#
# build-common.sh — shared helpers for building the system base rootfs images.
#
# Sourced by images/build-{ubuntu,alpine,arch,fedora}.sh. Each caller defines
# the distro base image, reserved template ID, and the in-container prep snippet
# (install packages + create wrenn-user), then calls build_system_rootfs.
#
# The same statically-linked envd + tini run on every distro; the per-OS prep
# only differs in the package manager and the user-creation command.

set -euo pipefail

# base36(all-zeros UUID) = the platform team that owns every system base
# template. Must match id.PlatformTeamID / id.UUIDToBase36 on the Go side.
PLATFORM_TEAM_B36="0000000000000000000000000"

# WRENN_SUDOERS_SETUP grants wrenn-user passwordless sudo. Identical on every
# distro; appended to each prep snippet after the user is created.
WRENN_SUDOERS_SETUP='echo "wrenn-user ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/wrenn-user && chmod 0440 /etc/sudoers.d/wrenn-user'

# build_system_rootfs <base_image> <template_id_int> <prep_snippet>
#
# Spawns a throwaway container from base_image, runs prep_snippet inside it,
# then exports it to the system base template's on-disk path
# (images/teams/<platform>/<base36(id)>/rootfs.ext4) via rootfs-from-container.sh.
build_system_rootfs() {
    local base_image="$1" template_id="$2" prep="$3"
    local script_dir project_root container dest tmpl_b36

    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    project_root="$(cd "${script_dir}/.." && pwd)"
    container="wrenn-build-${template_id}-$$"

    # base36(template_id). System IDs are single-digit (0-3), so base36 equals
    # the decimal digit and the 25-char zero-padded decimal matches what
    # id.UUIDToBase36 produces for these well-known IDs.
    tmpl_b36="$(printf '%025d' "${template_id}")"
    dest="teams/${PLATFORM_TEAM_B36}/${tmpl_b36}"

    echo "==> Pulling ${base_image}..."
    docker pull "${base_image}"

    echo "==> Preparing container ${container}..."
    docker rm -f "${container}" >/dev/null 2>&1 || true

    # Arm cleanup before starting the container so a failed run still removes it.
    # Expand the name into the trap now: it must survive after this function's
    # locals go out of scope (set -u would error on a stale reference otherwise).
    trap "docker rm -f '${container}' >/dev/null 2>&1 || true" EXIT

    docker run --name "${container}" "${base_image}" /bin/sh -c "${prep}"

    # Run the exporter as the normal user, NOT under sudo: it builds envd via
    # `make build-envd` (needs cargo on the user's PATH) and uses sudo itself
    # for the privileged mount/mkfs/copy steps.
    echo "==> Exporting to images/${dest}/rootfs.ext4..."
    bash "${project_root}/scripts/rootfs-from-container.sh" "${container}" "${dest}"
}
