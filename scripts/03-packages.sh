#!/bin/bash
# Stage 3: Install packages
set -euo pipefail

echo "[03-packages] Installing packages..."

# Mount pseudo-filesystems
mount --bind /proc "${ROOTFS_DIR}/proc"
mount --bind /sys "${ROOTFS_DIR}/sys"
mount --bind /dev "${ROOTFS_DIR}/dev"
mount --bind /dev/pts "${ROOTFS_DIR}/dev/pts"

cleanup() {
    umount -lf "${ROOTFS_DIR}/dev/pts" 2>/dev/null || true
    umount -lf "${ROOTFS_DIR}/dev"     2>/dev/null || true
    umount -lf "${ROOTFS_DIR}/sys"     2>/dev/null || true
    umount -lf "${ROOTFS_DIR}/proc"    2>/dev/null || true
}
trap cleanup EXIT

# Build combined package list from base + board-specific
PACKAGE_LISTS="${BUILD_DIR}/packages/base.list"
if [ -f "${BOARD_DIR}/packages.list" ]; then
    PACKAGE_LISTS="${PACKAGE_LISTS} ${BOARD_DIR}/packages.list"
fi

# Parse lists: strip comments and blank lines
PACKAGES=$(cat $PACKAGE_LISTS | grep -v '^\s*#' | grep -v '^\s*$' | tr '\n' ' ')

# Install firmware packages first (defined in board.conf)
if [ -n "${FIRMWARE_PACKAGES:-}" ]; then
    chroot "${ROOTFS_DIR}" bash -c "
        apt-get install -y --no-install-recommends ${FIRMWARE_PACKAGES}
    "
fi

# Install all other packages
chroot "${ROOTFS_DIR}" bash -c "
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ${PACKAGES}
"

# Strip size
chroot "${ROOTFS_DIR}" bash -c "
    apt-get clean
    rm -rf /usr/share/doc/*
    rm -rf /usr/share/man/*
    rm -rf /usr/share/locale/*
    rm -rf /var/cache/apt/archives/*.deb
    rm -rf /var/lib/apt/lists/*
"

echo "[03-packages] Done."
