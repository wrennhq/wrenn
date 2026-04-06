#!/bin/sh
# wrenn-init: minimal PID 1 init for Firecracker microVMs.
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

# Set hostname
hostname sandbox

# Configure networking from kernel cmdline (ip=client::gw:mask:host:iface:autoconf).
# if command -v ip >/dev/null 2>&1; then
#     iparg=$(cat /proc/cmdline | tr ' ' '\n' | sed -n 's/^ip=//p')
#     if [ -n "$iparg" ]; then
#         client=$(echo "$iparg" | cut -d: -f1)
#         gw=$(echo "$iparg" | cut -d: -f2)
#         mask=$(echo "$iparg" | cut -d: -f3)
#         iface=$(echo "$iparg" | cut -d: -f5)
#         [ -z "$iface" ] && iface=eth0
#         if [ -n "$client" ]; then
#             ip addr add "$client/${mask:-30}" dev "$iface" 2>/dev/null || true
#             ip link set "$iface" up 2>/dev/null || true
#             if [ -n "$gw" ]; then
#                 ip route add default via "$gw" 2>/dev/null || true
#             fi
#         fi
#     fi
# fi
#
#
if ! ip addr show eth0 2>/dev/null | grep -q "169.254.0.21"; then
    ip link set lo up
    ip link set eth0 up
    ip addr add 169.254.0.21/30 dev eth0
    ip route add default via 169.254.0.22
fi


# Configure DNS resolver.
echo "nameserver 8.8.8.8" > /etc/resolv.conf
echo "nameserver 8.8.4.4" >> /etc/resolv.conf

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
