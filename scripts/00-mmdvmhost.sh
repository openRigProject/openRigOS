#!/bin/bash
# Stage 0b: Build MMDVMHost from source for the target architecture.
# MMDVMHost is not in Debian repos — we cross-compile from GitHub.
# Output goes to /tmp/openrigos-bins/ alongside the Go binaries.
set -euo pipefail

STAGING_DIR="/tmp/openrigos-bins"
MMDVM_SRC="/tmp/mmdvmhost-src"

echo "[00-mmdvmhost] Building MMDVMHost (ARCH=${DEBIAN_ARCH})..."

rm -rf "${MMDVM_SRC}"
git clone --depth=1 https://github.com/g4klx/MMDVMHost.git "${MMDVM_SRC}"

cd "${MMDVM_SRC}"

# Select cross-compiler for target architecture
case "${DEBIAN_ARCH}" in
    arm64)
        TARGET_CXX=aarch64-linux-gnu-g++
        TARGET_CC=aarch64-linux-gnu-gcc
        ;;
    armhf)
        TARGET_CXX=arm-linux-gnueabihf-g++
        TARGET_CC=arm-linux-gnueabihf-gcc
        ;;
    amd64|*)
        TARGET_CXX=g++
        TARGET_CC=gcc
        ;;
esac

echo "[00-mmdvmhost] Using CXX=${TARGET_CXX}"
make CXX="${TARGET_CXX}" CC="${TARGET_CC}" -j"$(nproc)"

mkdir -p "${STAGING_DIR}"
cp MMDVMHost "${STAGING_DIR}/MMDVMHost"

# Stage license files for installation into /usr/share/doc/openrig/licenses/
mkdir -p "${STAGING_DIR}/licenses"
[ -f COPYING ] && cp COPYING "${STAGING_DIR}/licenses/MMDVMHost-COPYING.txt"
git rev-parse HEAD > "${STAGING_DIR}/licenses/MMDVMHost-SOURCE.txt"
echo "https://github.com/g4klx/MMDVMHost" >> "${STAGING_DIR}/licenses/MMDVMHost-SOURCE.txt"

echo "[00-mmdvmhost] Done. MMDVMHost staged in ${STAGING_DIR}"
