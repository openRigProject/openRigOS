#!/bin/bash
# Stage 2: Base system configuration
set -euo pipefail

echo "[02-configure] Configuring base system..."

# Mount pseudo-filesystems for chroot operations
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

# Pre-provisioning hostname — avahi will advertise openrig-config.local
# until provisioning sets a unique name based on callsign + device type
echo "openrig-config" > "${ROOTFS_DIR}/etc/hostname"

# Hosts
cat > "${ROOTFS_DIR}/etc/hosts" <<EOF
127.0.0.1   localhost
127.0.1.1   openrig-config
::1         localhost ip6-localhost ip6-loopback
ff02::1     ip6-allnodes
ff02::2     ip6-allrouters
EOF

# fstab — board-specific entries added by 05-bootloader.sh
cat > "${ROOTFS_DIR}/etc/fstab" <<EOF
proc            /proc           proc    defaults          0 0
sysfs           /sys            sysfs   defaults          0 0
tmpfs           /tmp            tmpfs   defaults,noatime  0 0
EOF

# APT sources
cat > "${ROOTFS_DIR}/etc/apt/sources.list" <<EOF
deb http://deb.debian.org/debian ${DEBIAN_SUITE} main contrib non-free non-free-firmware
deb http://security.debian.org/debian-security ${DEBIAN_SUITE}-security main contrib non-free non-free-firmware
deb http://deb.debian.org/debian ${DEBIAN_SUITE}-updates main contrib non-free non-free-firmware
EOF

# Add board-specific APT repos if defined
if [ -n "${EXTRA_REPOS:-}" ]; then
    echo "${EXTRA_REPOS}" >> "${ROOTFS_DIR}/etc/apt/sources.list"
fi

# Import extra repo GPG keys using the host gpg (not chroot — gpg isn't
# installed in the minimal debootstrap rootfs at this stage)
if [ -n "${EXTRA_REPO_KEY:-}" ]; then
    mkdir -p "${ROOTFS_DIR}/etc/apt/trusted.gpg.d"
    wget -qO- "${EXTRA_REPO_KEY}" | \
        gpg --dearmor > "${ROOTFS_DIR}/etc/apt/trusted.gpg.d/extra-repo.gpg"
fi

# Locale
DEBIAN_FRONTEND=noninteractive chroot "${ROOTFS_DIR}" bash -c "
    apt-get update -q
    apt-get install -y locales sudo
    echo 'en_US.UTF-8 UTF-8' > /etc/locale.gen
    locale-gen
    update-locale LANG=en_US.UTF-8
"

# Timezone
chroot "${ROOTFS_DIR}" bash -c "
    ln -sf /usr/share/zoneinfo/UTC /etc/localtime
    echo UTC > /etc/timezone
"

# Root password (locked — use SSH keys or add a user in overlay)
chroot "${ROOTFS_DIR}" passwd -l root

# Create default ham radio user
# dialout and audio groups may not exist yet; they'll be created when
# the relevant packages are installed in stage 03. We add them here
# and useradd will skip unknown groups with --groups not --G
chroot "${ROOTFS_DIR}" bash -c "
    # Ensure sudoers.d exists (sudo just installed above)
    mkdir -p /etc/sudoers.d

    useradd -m -s /bin/bash openrig
    echo 'openrig:openrig' | chpasswd

    # Force password change on first login
    chage -d 0 openrig

    echo '%sudo ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/openrig
    chmod 440 /etc/sudoers.d/openrig

    # Add to sudo group (exists now that sudo is installed)
    usermod -aG sudo openrig
"

# --- Overlays (order matters: common first, board second so board wins) ---

# 1. Apply common overlay (includes /etc/openrig.json base template)
COMMON_OVERLAY="${BUILD_DIR}/overlay/common"
if [ -d "${COMMON_OVERLAY}" ]; then
    echo "[02-configure] Applying common overlay..."
    rsync -a "${COMMON_OVERLAY}/" "${ROOTFS_DIR}/"
fi

# 2. Inject build-time board values into /etc/openrig.json
OPENRIG_JSON="${ROOTFS_DIR}/etc/openrig.json"
if [ -f "${OPENRIG_JSON}" ]; then
    echo "[02-configure] Injecting board values into /etc/openrig.json..."
    sed -i \
        -e "s|\"__BOARD__\"|\"${BOARD}\"|g" \
        -e "s|\"__ARCH__\"|\"${ARCH}\"|g" \
        -e "s|\"__DEVICE_TYPE__\"|\"${DEVICE_TYPE:-}\"|g" \
        "${OPENRIG_JSON}"

    # Inject optional feature flags from board.conf
    if [ "${USB_GADGET_ENABLED:-false}" = "true" ]; then
        jq '.openrig.network.usb_gadget.enabled = true' "${OPENRIG_JSON}" \
            > "${OPENRIG_JSON}.tmp" && mv "${OPENRIG_JSON}.tmp" "${OPENRIG_JSON}"
    fi
fi

# 3. Apply board-specific overlay (may override/extend openrig.json sections)
if [ -d "${BOARD_DIR}/overlay" ]; then
    echo "[02-configure] Applying board overlay..."
    rsync -a "${BOARD_DIR}/overlay/" "${ROOTFS_DIR}/"
fi

# 4. Ensure correct permissions on openrig.json — must be writable by openrig user
if [ -f "${OPENRIG_JSON}" ]; then
    chmod 644 "${OPENRIG_JSON}"
    chroot "${ROOTFS_DIR}" chown openrig:openrig /etc/openrig.json
fi

# 4b. Ensure openrig user can write MMDVM/gateway ini files (API runs as openrig,
#     NoNewPrivileges=true prevents sudo inside the service).
for dir in /etc/mmdvm /etc/dmrgateway /etc/ysfgateway; do
    if [ -d "${ROOTFS_DIR}${dir}" ]; then
        chroot "${ROOTFS_DIR}" chown -R openrig:openrig "${dir}"
    fi
done

# 5. Create runtime directories required by services before first boot
#    (ProtectSystem=strict needs ReadWritePaths to exist at service start)
chroot "${ROOTFS_DIR}" bash -c "
    mkdir -p /var/log/openrig
    chown openrig:openrig /var/log/openrig
    chmod 755 /var/log/openrig
"

echo "[02-configure] Done."
