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

# Hostname
echo "openrigos" > "${ROOTFS_DIR}/etc/hostname"

# Hosts
cat > "${ROOTFS_DIR}/etc/hosts" <<EOF
127.0.0.1   localhost
127.0.1.1   openrigos
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

# Import extra repo GPG keys
if [ -n "${EXTRA_REPO_KEY:-}" ]; then
    wget -qO- "${EXTRA_REPO_KEY}" | \
        chroot "${ROOTFS_DIR}" gpg --dearmor -o /etc/apt/trusted.gpg.d/extra-repo.gpg
fi

# Locale
chroot "${ROOTFS_DIR}" bash -c "
    apt-get update -q
    apt-get install -y locales
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
chroot "${ROOTFS_DIR}" bash -c "
    useradd -m -s /bin/bash -G sudo,dialout,audio openrig
    echo 'openrig:openrig' | chpasswd
    echo '%sudo ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers.d/openrig
"

# Apply board-specific overlay files
if [ -d "${BOARD_DIR}/overlay" ]; then
    echo "[02-configure] Applying board overlay..."
    rsync -a "${BOARD_DIR}/overlay/" "${ROOTFS_DIR}/"
fi

echo "[02-configure] Done."
