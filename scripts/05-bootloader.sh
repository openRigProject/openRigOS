#!/bin/bash
# Stage 5: Bootloader setup (board-specific)
set -euo pipefail

echo "[05-bootloader] Setting up bootloader: ${BOOTLOADER}..."

case "${BOOTLOADER}" in

    rpi-firmware)
        # Raspberry Pi uses GPU firmware as first-stage bootloader.
        # config.txt and cmdline.txt are already placed via the board overlay.
        # Add SD card partition entries to fstab.
        cat >> "${ROOTFS_DIR}/etc/fstab" <<EOF
/dev/mmcblk0p1  /boot           vfat    defaults          0 2
/dev/mmcblk0p2  /               ext4    defaults,noatime  0 1
EOF
        ;;

    grub-efi)
        # GRUB is installed into the image during 06-image.sh
        # Just add fstab entries here
        cat >> "${ROOTFS_DIR}/etc/fstab" <<EOF
/dev/sda1       /boot/efi       vfat    umask=0077        0 1
/dev/sda2       /               ext4    defaults,noatime  0 1
EOF
        ;;

    u-boot)
        # Generic U-Boot — board-specific U-Boot package defined in board.conf
        mount --bind /proc "${ROOTFS_DIR}/proc"
        mount --bind /sys  "${ROOTFS_DIR}/sys"
        mount --bind /dev  "${ROOTFS_DIR}/dev"
        cleanup() {
            umount -lf "${ROOTFS_DIR}/dev" 2>/dev/null || true
            umount -lf "${ROOTFS_DIR}/sys" 2>/dev/null || true
            umount -lf "${ROOTFS_DIR}/proc" 2>/dev/null || true
        }
        trap cleanup EXIT
        chroot "${ROOTFS_DIR}" bash -c "
            DEBIAN_FRONTEND=noninteractive apt-get install -y ${UBOOT_PACKAGE:-u-boot-tools}
        "
        cat >> "${ROOTFS_DIR}/etc/fstab" <<EOF
/dev/mmcblk0p1  /boot           vfat    defaults          0 2
/dev/mmcblk0p2  /               ext4    defaults,noatime  0 1
EOF
        ;;

    *)
        echo "ERROR: Unknown bootloader: ${BOOTLOADER}"
        exit 1
        ;;
esac

echo "[05-bootloader] Done."
