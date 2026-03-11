#!/bin/sh
# wrenn-init: minimal PID 1 init for Firecracker microVMs.
# Mounts virtual filesystems then execs envd.

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

# Set hostname
hostname sandbox

# Configure DNS resolver.
echo "nameserver 8.8.8.8" > /etc/resolv.conf
echo "nameserver 8.8.4.4" >> /etc/resolv.conf

# Exec envd as the main process (replaces this script, keeps PID 1).
exec /usr/local/bin/envd
