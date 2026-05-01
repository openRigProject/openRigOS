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

# Install Go binaries and WASM assets from staging area (compiled in stage 00)
STAGING_DIR="/tmp/openrigos-bins"
if [ -d "${STAGING_DIR}" ]; then
    echo "[07-services] Installing binaries and assets..."
    mkdir -p "${ROOTFS_DIR}/usr/local/bin"
    mkdir -p "${ROOTFS_DIR}/usr/local/lib/openrig"
    # WASM assets → /usr/local/lib/openrig (not executable)
    for f in openrig.wasm wasm_exec.js; do
        [ -f "${STAGING_DIR}/${f}" ] && cp "${STAGING_DIR}/${f}" "${ROOTFS_DIR}/usr/local/lib/openrig/${f}"
    done
    # Everything else → /usr/local/bin (native executables)
    for f in "${STAGING_DIR}/"*; do
        base="$(basename "$f")"
        [ -f "$f" ] && [ "$base" != "openrig.wasm" ] && [ "$base" != "wasm_exec.js" ] \
            && cp "$f" "${ROOTFS_DIR}/usr/local/bin/" \
            && chmod +x "${ROOTFS_DIR}/usr/local/bin/${base}"
    done
    # Third-party license files → /usr/share/doc/openrig/licenses/
    if [ -d "${STAGING_DIR}/licenses" ]; then
        mkdir -p "${ROOTFS_DIR}/usr/share/doc/openrig/licenses"
        cp "${STAGING_DIR}/licenses/"* "${ROOTFS_DIR}/usr/share/doc/openrig/licenses/"
    fi
fi

# Download YSF/FCS host lists into the image
mkdir -p "${ROOTFS_DIR}/usr/local/etc"
echo "[07-services] Downloading YSFHosts.json..."
_ysf_tmp=$(mktemp)
if curl --fail --silent -S -L -A "openRig - G4KLX" \
    https://hostfiles.refcheck.radio/YSFHosts.json \
    | jq '{reflectors:[.reflectors[]|{designator,country,name,use_xx_prefix,description:(.description//""),user_count:(.user_count//"000"),port:(.port//42000),ipv4:(.ipv4//null),ipv6:(.ipv6//null)}]}' \
    > "${_ysf_tmp}" && [ -s "${_ysf_tmp}" ]; then
    cp "${_ysf_tmp}" "${ROOTFS_DIR}/usr/local/etc/YSFHosts.json"
    echo "[07-services]   YSFHosts.json downloaded ($(wc -c < "${_ysf_tmp}") bytes)"
else
    echo "[07-services]   WARNING: YSFHosts.json download failed — skipping (existing file preserved)"
fi
rm -f "${_ysf_tmp}"
chroot "${ROOTFS_DIR}" chown openrig:openrig /usr/local/etc/YSFHosts.json 2>/dev/null || true
echo "[07-services] Downloading FCSHosts.txt..."
curl --fail --silent -S -L \
    -o "${ROOTFS_DIR}/usr/local/etc/FCSHosts.txt" \
    https://www.pistar.uk/downloads/FCS_Hosts.txt \
    || echo "[07-services]   WARNING: FCSHosts.txt download failed — file will be empty"

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

enable_service openrig-display-boot.service
enable_service openrig-display-shutdown.service
enable_service openrig-display.service
enable_service mosquitto.service
enable_service openrig-hosts-update.timer
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
enable_service systemd-timesyncd.service

# Mask dnsmasq.service — we start per-interface instances manually.
# Use direct symlink rather than 'systemctl mask' (chroot has no systemd daemon).
ln -sf /dev/null "${ROOTFS_DIR}/etc/systemd/system/dnsmasq.service"
rm -f "${ROOTFS_DIR}/etc/systemd/system/multi-user.target.wants/dnsmasq.service"
echo "[07-services]   masked dnsmasq.service (direct symlink)"

echo "[07-services] Done."
