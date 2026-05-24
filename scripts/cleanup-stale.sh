#!/usr/bin/env bash
# Clean up leftover wrenn host state from crashed/unclean agent exits.
# Removes: cloud-hypervisor procs, CH sockets, dm-snapshot devices,
# loop devices backing sandbox CoW files AND base rootfs images,
# network namespaces, veth interfaces, iptables rules, sandbox CoW
# files (optional).
#
# Does NOT touch: /var/lib/wrenn/images/* files themselves,
# /var/lib/wrenn/kernels/*, snapshot directories (cl-*/ with state.json).
#
# WARNING: base rootfs image loop devices are detached unconditionally.
# Run only when no wrenn-agent is alive — a live agent holds those.
#
# Sudo is invoked per-command. Script itself runs as normal user.
#
# Usage: bash scripts/cleanup-stale.sh [--delete-cow]

set -u

DELETE_COW=0
for arg in "$@"; do
  case "$arg" in
    --delete-cow) DELETE_COW=1 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

SANDBOX_DIR=/var/lib/wrenn/sandboxes

log() { printf '[cleanup] %s\n' "$*"; }

# Prime sudo once so subsequent calls don't re-prompt.
sudo -v || { echo "sudo required" >&2; exit 1; }

# 1. Kill leftover cloud-hypervisor processes.
# Match via -f because the proc comm is truncated to 15 chars ("cloud-hypervisor" is 16).
if pgrep -f '/cloud-hypervisor( |$)' >/dev/null; then
  log "killing cloud-hypervisor procs"
  sudo pkill -TERM -f '/cloud-hypervisor( |$)' || true
  for _ in 1 2 3 4 5; do
    pgrep -f '/cloud-hypervisor( |$)' >/dev/null || break
    sleep 1
  done
  sudo pkill -KILL -f '/cloud-hypervisor( |$)' 2>/dev/null || true
  sleep 1
fi

# 2. Remove stale CH API sockets.
for sock in /tmp/ch-*.sock; do
  [[ -e "$sock" ]] || continue
  log "rm $sock"
  sudo rm -f "$sock"
done

# 3. Remove all dm-snapshot devices with wrenn- prefix.
while read -r name _; do
  [[ -z "$name" || "$name" == "No" ]] && continue
  case "$name" in
    wrenn-*)
      log "dmsetup remove $name"
      sudo dmsetup remove --retry "$name" || sudo dmsetup remove --force "$name" || true
      ;;
  esac
done < <(sudo dmsetup ls --target snapshot 2>/dev/null)

# 4. Detach loop devices backing sandbox CoW files and base rootfs images.
IMAGES_DIR=/var/lib/wrenn/images
while IFS= read -r line; do
  dev=${line%%:*}
  backing=${line#*(}              # strip up to first '('
  backing=${backing%)}            # strip trailing ')'
  backing=${backing% (deleted)}   # strip kernel '(deleted)' marker
  case "$backing" in
    "$SANDBOX_DIR"/*.cow|"$IMAGES_DIR"/*)
      log "losetup -d $dev ($backing)"
      sudo losetup -d "$dev" || true
      ;;
  esac
done < <(losetup -a)

# 5. Tear down wrenn network namespaces + host veth.
if [[ -d /run/netns ]]; then
  for ns in /run/netns/wrenn-ns-*; do
    [[ -e "$ns" ]] || continue
    name=$(basename "$ns")
    idx=${name#wrenn-ns-}
    veth="wrenn-veth-$idx"
    log "deleting netns $name and host veth $veth"
    sudo ip link del "$veth" 2>/dev/null || true
    sudo ip netns del "$name" || true
  done
fi

# 6. Strip any remaining wrenn-veth interfaces.
for link in $(ip -o link show | awk -F': ' '{print $2}' | cut -d@ -f1 | grep '^wrenn-veth-' || true); do
  log "ip link del $link"
  sudo ip link del "$link" 2>/dev/null || true
done

# 7. Remove host iptables rules referencing wrenn-veth interfaces.
for table in filter nat; do
  sudo iptables-save -t "$table" 2>/dev/null | grep 'wrenn-veth-' | while read -r line; do
    [[ "$line" == -A* ]] || continue
    log "iptables -t $table -D ${line#-A }"
    # shellcheck disable=SC2086
    sudo iptables -t "$table" -D ${line#-A } 2>/dev/null || true
  done
done

# 8. Optionally delete sandbox CoW files.
if (( DELETE_COW )); then
  for f in "$SANDBOX_DIR"/*.cow; do
    [[ -e "$f" ]] || continue
    log "rm $f"
    sudo rm -f "$f"
  done
fi

log "done"
