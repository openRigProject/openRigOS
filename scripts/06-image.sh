#!/bin/bash
# Stage 6: Create output image
set -euo pipefail

echo "[06-image] Creating ${IMAGE_TYPE} image..."

IMAGE_NAME="openRigOS-${BOARD}-${DEBIAN_ARCH}"
IMAGE_PATH="${OUTPUT_DIR}/${IMAGE_NAME}.${IMAGE_TYPE}"

case "${IMAGE_TYPE}" in

    img)
        # Raw disk image for SD card (RPi and most SoCs)
        IMAGE_SIZE="${IMAGE_SIZE:-2G}"

        # Create empty image file
        rm -f "${IMAGE_PATH}"
        fallocate -l "${IMAGE_SIZE}" "${IMAGE_PATH}"

        # Partition: 256MB FAT32 boot + rest ext4 root
        parted -s "${IMAGE_PATH}" \
            mklabel msdos \
            mkpart primary fat32 1MiB 257MiB \
            mkpart primary ext4  257MiB 100% \
            set 1 boot on

        # Mount via loop device + kpartx (works in Docker Desktop on Mac)
        LOOP=$(losetup -f --show "${IMAGE_PATH}")
        kpartx -av "${LOOP}"
        LOOP_NAME=$(basename "${LOOP}")
        BOOT_PART="/dev/mapper/${LOOP_NAME}p1"
        ROOT_PART="/dev/mapper/${LOOP_NAME}p2"

        # Give udev/kernel a moment to create the device nodes
        sleep 1

        cleanup() {
            umount -lf /mnt/openrigos-boot 2>/dev/null || true
            umount -lf /mnt/openrigos-root 2>/dev/null || true
            kpartx -dv "${LOOP}"           2>/dev/null || true
            losetup -d "${LOOP}"           2>/dev/null || true
        }
        trap cleanup EXIT

        # Format partitions
        mkfs.vfat -F 32 -n BOOT "${BOOT_PART}"
        mkfs.ext4 -L openrigos-root "${ROOT_PART}"

        # Mount and copy rootfs
        mkdir -p /mnt/openrigos-root /mnt/openrigos-boot
        mount "${ROOT_PART}" /mnt/openrigos-root
        mount "${BOOT_PART}" /mnt/openrigos-boot

        rsync -aHAX --exclude='/boot/*' "${ROOTFS_DIR}/" /mnt/openrigos-root/
        rsync -aHAX "${ROOTFS_DIR}/boot/" /mnt/openrigos-boot/

        # Update fstab with real PARTUUIDs
        BOOT_UUID=$(blkid -s UUID -o value "${BOOT_PART}")
        ROOT_UUID=$(blkid -s UUID -o value "${ROOT_PART}")
        sed -i "s|/dev/mmcblk0p1|UUID=${BOOT_UUID}|g" /mnt/openrigos-root/etc/fstab
        sed -i "s|/dev/mmcblk0p2|UUID=${ROOT_UUID}|g" /mnt/openrigos-root/etc/fstab

        # Leave cmdline.txt root as /dev/mmcblk0p2 — device node is simpler
        # and more reliable than UUID on RPi (UUID can mismatch after partial flashes)

        sync
        echo "[06-image] Image written: ${IMAGE_PATH}"
        ;;

    iso)
        # Bootable ISO for x86_64
        ISO_STAGING="/tmp/openrigos-iso"
        rm -rf "${ISO_STAGING}"
        mkdir -p "${ISO_STAGING}/boot/grub"

        # Compress rootfs into squashfs
        mkdir -p "${ISO_STAGING}/live"
        mksquashfs "${ROOTFS_DIR}" "${ISO_STAGING}/live/filesystem.squashfs" \
            -comp xz -e boot

        # Copy kernel and initrd — resolve versioned filenames if plain symlinks are absent
        VMLINUZ=$(ls "${ROOTFS_DIR}/boot/vmlinuz" 2>/dev/null \
            || ls "${ROOTFS_DIR}/boot/vmlinuz"-* 2>/dev/null | sort -V | tail -1)
        INITRD=$(ls "${ROOTFS_DIR}/boot/initrd.img" 2>/dev/null \
            || ls "${ROOTFS_DIR}/boot/initrd.img"-* 2>/dev/null | sort -V | tail -1)
        [ -z "${VMLINUZ}" ] && { echo "[06-image] ERROR: no vmlinuz found in ${ROOTFS_DIR}/boot/"; exit 1; }
        [ -z "${INITRD}"  ] && { echo "[06-image] ERROR: no initrd.img found in ${ROOTFS_DIR}/boot/"; exit 1; }
        cp "${VMLINUZ}" "${ISO_STAGING}/boot/vmlinuz"
        cp "${INITRD}"  "${ISO_STAGING}/boot/initrd.img"

        # GRUB config
        cat > "${ISO_STAGING}/boot/grub/grub.cfg" <<GRUBEOF
set default=0
set timeout=5

menuentry "openRigOS" {
    linux  /boot/vmlinuz boot=live quiet
    initrd /boot/initrd.img
}
GRUBEOF

        # Build ISO
        grub-mkrescue -o "${IMAGE_PATH}" "${ISO_STAGING}"
        echo "[06-image] ISO written: ${IMAGE_PATH}"
        ;;

    *)
        echo "ERROR: Unknown image type: ${IMAGE_TYPE}"
        exit 1
        ;;
esac
