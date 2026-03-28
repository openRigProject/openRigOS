#!/bin/bash
# Stage 1: Bootstrap minimal Debian rootfs
set -euo pipefail

echo "[01-bootstrap] Bootstrapping Debian ${DEBIAN_SUITE} for ${DEBIAN_ARCH}..."

rm -rf "${ROOTFS_DIR}"
mkdir -p "${ROOTFS_DIR}"

# Copy qemu static binary so the chroot can run ARM binaries
QEMU_BIN="/usr/bin/qemu-${QEMU_ARCH}-static"
if [ "${QEMU_ARCH}" != "x86_64" ]; then
    if [ ! -f "${QEMU_BIN}" ]; then
        echo "ERROR: ${QEMU_BIN} not found — QEMU user-static not installed"
        exit 1
    fi
fi

# First stage: download only
debootstrap \
    --arch="${DEBIAN_ARCH}" \
    --foreign \
    "${DEBIAN_SUITE}" \
    "${ROOTFS_DIR}" \
    http://deb.debian.org/debian

# Install QEMU into rootfs before second stage
if [ "${QEMU_ARCH}" != "x86_64" ]; then
    cp "${QEMU_BIN}" "${ROOTFS_DIR}/usr/bin/"
fi

# Second stage: runs inside the chroot (via QEMU for ARM)
chroot "${ROOTFS_DIR}" /debootstrap/debootstrap --second-stage

echo "[01-bootstrap] Done."
