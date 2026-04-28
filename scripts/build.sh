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
set -a   # auto-export all variables defined from here
source "${BOARD_DIR}/board.conf"
set +a

echo "======================================"
echo " openRigOS Build System"
echo " Board : ${BOARD}"
echo " Arch  : ${ARCH} (${DEBIAN_ARCH})"
echo " Suite : ${DEBIAN_SUITE}"
echo " Output: ${OUTPUT_DIR}"
echo "======================================"

export BOARD BOARD_DIR BUILD_DIR ROOTFS_DIR OUTPUT_DIR GOARCH

# Volume-mounted scripts may lose execute bit — ensure they're all executable
chmod +x "${BUILD_DIR}/scripts/"*.sh

# Run build stages
"${BUILD_DIR}/scripts/00-gobuilds.sh"   # compile Go binaries → /tmp/openrigos-bins/
"${BUILD_DIR}/scripts/00-mmdvmhost.sh"  # compile MMDVMHost → /tmp/openrigos-bins/
"${BUILD_DIR}/scripts/01-gateways.sh"   # compile DMRGateway + YSFGateway → /tmp/openrigos-bins/
"${BUILD_DIR}/scripts/01-bootstrap.sh"  # creates ROOTFS_DIR fresh
"${BUILD_DIR}/scripts/02-configure.sh"
"${BUILD_DIR}/scripts/03-packages.sh"
"${BUILD_DIR}/scripts/04-kernel.sh"
"${BUILD_DIR}/scripts/05-bootloader.sh"
"${BUILD_DIR}/scripts/07-services.sh"
"${BUILD_DIR}/scripts/06-image.sh"

echo ""
echo "Build complete."
echo "Output: ${OUTPUT_DIR}/openRigOS-${BOARD}-${DEBIAN_ARCH}.${IMAGE_TYPE}"
