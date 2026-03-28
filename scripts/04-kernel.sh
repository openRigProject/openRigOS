#!/bin/bash
# Stage 4: Install kernel
set -euo pipefail

echo "[04-kernel] Installing kernel: ${KERNEL_PACKAGE}..."

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

chroot "${ROOTFS_DIR}" bash -c "
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
        ${KERNEL_PACKAGE} \
        ${KERNEL_HEADERS:-} \
        initramfs-tools
"

echo "[04-kernel] Done."
