#!/bin/bash
# Stage 7: Enable systemd services
set -euo pipefail

echo "[07-services] Enabling openRigOS services..."

mount --bind /proc "${ROOTFS_DIR}/proc"
mount --bind /sys  "${ROOTFS_DIR}/sys"
mount --bind /dev  "${ROOTFS_DIR}/dev"

cleanup() {
    umount -lf "${ROOTFS_DIR}/dev" 2>/dev/null || true
    umount -lf "${ROOTFS_DIR}/sys" 2>/dev/null || true
    umount -lf "${ROOTFS_DIR}/proc" 2>/dev/null || true
}
trap cleanup EXIT

# Install Go binaries from staging area (compiled in stage 00)
STAGING_DIR="/tmp/openrigos-bins"
if [ -d "${STAGING_DIR}" ]; then
    echo "[07-services] Installing Go binaries..."
    mkdir -p "${ROOTFS_DIR}/usr/local/bin"
    cp "${STAGING_DIR}/"* "${ROOTFS_DIR}/usr/local/bin/"
    chmod +x "${ROOTFS_DIR}/usr/local/bin/"*
fi

# Make openrig scripts executable
chmod +x "${ROOTFS_DIR}/usr/local/lib/openrig/"*.sh 2>/dev/null || true
chmod +x "${ROOTFS_DIR}/usr/local/bin/openrig-"*   2>/dev/null || true

# Fix home directory ownership (rsync may have set it to root)
chroot "${ROOTFS_DIR}" chown -R openrig:openrig /home/openrig

# Enable services — use || true so a missing unit doesn't abort the build
enable_service() {
    chroot "${ROOTFS_DIR}" systemctl enable "$1" 2>&1 \
        && echo "[07-services]   enabled $1" \
        || echo "[07-services]   SKIP $1 (not installed)"
}

enable_service openrig-wifi.service
enable_service avahi-daemon.service
enable_service openrig-usb-gadget.service
enable_service openrig-provision-web.service
enable_service openrig-rigctld.service
enable_service openrig-api.service
enable_service openrig-mmdvmhost.service
enable_service openrig-dmrgateway.service
enable_service openrig-ysfgateway.service
enable_service ssh.service
enable_service systemd-networkd.service
enable_service systemd-resolved.service

# Mask dnsmasq.service — we start per-interface instances manually.
# Use direct symlink rather than 'systemctl mask' (chroot has no systemd daemon).
ln -sf /dev/null "${ROOTFS_DIR}/etc/systemd/system/dnsmasq.service"
rm -f "${ROOTFS_DIR}/etc/systemd/system/multi-user.target.wants/dnsmasq.service"
echo "[07-services]   masked dnsmasq.service (direct symlink)"

echo "[07-services] Done."
