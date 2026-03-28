#!/bin/bash
# openRigOS main build entrypoint
set -euo pipefail

BOARD="${BOARD:-rpizero2w}"
BUILD_DIR="/build"
ROOTFS_DIR="/tmp/openrigos-rootfs"
OUTPUT_DIR="/output"

# Load board config
BOARD_DIR="${BUILD_DIR}/boards/${BOARD}"
if [ ! -f "${BOARD_DIR}/board.conf" ]; then
    echo "ERROR: Board config not found: ${BOARD_DIR}/board.conf"
    exit 1
fi
source "${BOARD_DIR}/board.conf"

echo "======================================"
echo " openRigOS Build System"
echo " Board : ${BOARD}"
echo " Arch  : ${ARCH} (${DEBIAN_ARCH})"
echo " Suite : ${DEBIAN_SUITE}"
echo " Output: ${OUTPUT_DIR}"
echo "======================================"

export BOARD BOARD_DIR BUILD_DIR ROOTFS_DIR OUTPUT_DIR

# Run build stages
"${BUILD_DIR}/scripts/01-bootstrap.sh"
"${BUILD_DIR}/scripts/02-configure.sh"
"${BUILD_DIR}/scripts/03-packages.sh"
"${BUILD_DIR}/scripts/04-kernel.sh"
"${BUILD_DIR}/scripts/05-bootloader.sh"
"${BUILD_DIR}/scripts/06-image.sh"

echo ""
echo "Build complete."
echo "Output: ${OUTPUT_DIR}/openRigOS-${BOARD}-${DEBIAN_ARCH}.${IMAGE_TYPE}"
