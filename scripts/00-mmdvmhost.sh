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

# Cross-compile for the target architecture
case "${DEBIAN_ARCH}" in
    arm64)
        export CXX=aarch64-linux-gnu-g++
        export CC=aarch64-linux-gnu-gcc
        ;;
    armhf)
        export CXX=arm-linux-gnueabihf-g++
        export CC=arm-linux-gnueabihf-gcc
        ;;
    amd64)
        # Native build — no cross-compiler needed
        ;;
    *)
        echo "[00-mmdvmhost] WARNING: unknown arch ${DEBIAN_ARCH}, attempting native build"
        ;;
esac

make -j"$(nproc)"

mkdir -p "${STAGING_DIR}"
cp MMDVMHost "${STAGING_DIR}/MMDVMHost"

echo "[00-mmdvmhost] Done. MMDVMHost staged in ${STAGING_DIR}"
