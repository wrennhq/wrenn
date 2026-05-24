#!/bin/sh
# wrenn-init: minimal PID 1 init for Cloud Hypervisor microVMs.
# Mounts virtual filesystems, starts chronyd for time sync, then execs tini + envd.

set -e

# Mount essential virtual filesystems if not already mounted.
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mkdir -p /dev/pts /dev/shm
mount -t devpts devpts /dev/pts 2>/dev/null || true
mount -t tmpfs tmpfs /dev/shm 2>/dev/null || true
mount -t tmpfs tmpfs /tmp 2>/dev/null || true
mount -t tmpfs tmpfs /run 2>/dev/null || true
mkdir -p /sys/fs/cgroup
mount -t cgroup2 cgroup2 /sys/fs/cgroup 2>/dev/null || true
echo "+cpu +memory +io" > /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null || true

# Disable write_zeroes and discard on rootfs — dm-snapshot doesn't support
# these ops, but CH advertises them anyway. Suppress at block queue level.
# sysfs attributes are read-only on some kernels, so failures are expected.
{ echo 0 > /sys/block/vda/queue/write_zeroes_max_bytes; } 2>/dev/null || true
{ echo 0 > /sys/block/vda/queue/discard_max_bytes; } 2>/dev/null || true

# Set hostname and make it resolvable (sudo requires this). Use the kernel knob
# directly so we don't depend on the `hostname` binary, which is absent from
# minimal Arch/Fedora images. Guard so a failure never aborts init under set -e.
echo capsule > /proc/sys/kernel/hostname 2>/dev/null || hostname capsule 2>/dev/null || true
echo "127.0.0.1 capsule" >> /etc/hosts 2>/dev/null || true

# Configure networking if the kernel ip= boot arg did not already set it up.
if ! ip addr show eth0 2>/dev/null | grep -q "169.254.0.21"; then
    ip link set lo up 2>/dev/null || true
    ip link set eth0 up 2>/dev/null || true
    ip addr add 169.254.0.21/30 dev eth0 2>/dev/null || true
    ip route add default via 169.254.0.22 2>/dev/null || true
fi

# Configure DNS resolver. Drop any existing symlink first — on some distros
# (e.g. Fedora) /etc/resolv.conf is a dangling symlink into systemd-resolved,
# and writing through it would fail and abort init under set -e.
rm -f /etc/resolv.conf 2>/dev/null || true
{
    echo "nameserver 8.8.8.8"
    echo "nameserver 8.8.4.4"
} > /etc/resolv.conf 2>/dev/null || true

# Set a standard PATH so envd and all child processes can find common binaries.
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games

# Write chrony config to sync time from the KVM PTP hardware clock.
# /dev/ptp0 is a paravirtual clock exposed by KVM — no network required.
mkdir -p /etc/chrony /run/chrony
cat > /etc/chrony/chrony.conf <<EOF
refclock PHC /dev/ptp0 poll 2 dpoll 2
driftfile /run/chrony/chrony.drift
makestep 1.0 -1
EOF

# Start chronyd in the background before handing off to tini.
chronyd -f /etc/chrony/chrony.conf 2>/dev/null || true

# Exec tini as PID 1 — it reaps zombie processes and forwards signals to envd.
exec /sbin/tini -- /usr/local/bin/envd
