#!/bin/bash
# Stage 1: Cross-compile DMRGateway and YSFGateway from source.
# Neither is in Debian repos — we build from GitHub.
# Output goes to /tmp/openrigos-bins/ alongside Go and MMDVMHost binaries.
set -euo pipefail

STAGING_DIR="/tmp/openrigos-bins"
SRC_BASE="/tmp/gateway-builds"

mkdir -p "${STAGING_DIR}"
rm -rf "${SRC_BASE}"
mkdir -p "${SRC_BASE}"

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
        echo "[01-gateways] WARNING: unknown arch ${DEBIAN_ARCH}, attempting native build"
        ;;
esac

# ── DMRGateway ────────────────────────────────────────────────────────────
echo "[01-gateways] Building DMRGateway (ARCH=${DEBIAN_ARCH})..."
git clone --depth=1 https://github.com/g4klx/DMRGateway.git "${SRC_BASE}/DMRGateway"
cd "${SRC_BASE}/DMRGateway"
make -j"$(nproc)"
cp DMRGateway "${STAGING_DIR}/DMRGateway"
echo "[01-gateways] DMRGateway built."

# ── YSFGateway (from YSFClients repo) ────────────────────────────────────
echo "[01-gateways] Building YSFGateway (ARCH=${DEBIAN_ARCH})..."
git clone --depth=1 https://github.com/g4klx/YSFClients.git "${SRC_BASE}/YSFClients"
cd "${SRC_BASE}/YSFClients/YSFGateway"
make -j"$(nproc)"
cp YSFGateway "${STAGING_DIR}/YSFGateway"
echo "[01-gateways] YSFGateway built."

echo "[01-gateways] Done. Gateway binaries staged in ${STAGING_DIR}"
