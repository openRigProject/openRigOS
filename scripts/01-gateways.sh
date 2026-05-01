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

echo "[01-gateways] Using CXX=${TARGET_CXX}"

# ── DMRGateway ────────────────────────────────────────────────────────────
echo "[01-gateways] Building DMRGateway (ARCH=${DEBIAN_ARCH})..."
git clone --depth=1 https://github.com/g4klx/DMRGateway.git "${SRC_BASE}/DMRGateway"
cd "${SRC_BASE}/DMRGateway"
make CXX="${TARGET_CXX}" CC="${TARGET_CC}" -j"$(nproc)"
cp DMRGateway "${STAGING_DIR}/DMRGateway"
mkdir -p "${STAGING_DIR}/licenses"
[ -f COPYING ] && cp COPYING "${STAGING_DIR}/licenses/DMRGateway-COPYING.txt"
git rev-parse HEAD > "${STAGING_DIR}/licenses/DMRGateway-SOURCE.txt"
echo "https://github.com/g4klx/DMRGateway" >> "${STAGING_DIR}/licenses/DMRGateway-SOURCE.txt"
echo "[01-gateways] DMRGateway built."

# ── YSFGateway (from YSFClients repo) ────────────────────────────────────
echo "[01-gateways] Building YSFGateway (ARCH=${DEBIAN_ARCH})..."
git clone --depth=1 https://github.com/g4klx/YSFClients.git "${SRC_BASE}/YSFClients"
cd "${SRC_BASE}/YSFClients/YSFGateway"
make CXX="${TARGET_CXX}" CC="${TARGET_CC}" -j"$(nproc)"
cp YSFGateway "${STAGING_DIR}/YSFGateway"
mkdir -p "${STAGING_DIR}/licenses"
[ -f ../COPYING ] && cp ../COPYING "${STAGING_DIR}/licenses/YSFGateway-COPYING.txt"
git -C "${SRC_BASE}/YSFClients" rev-parse HEAD > "${STAGING_DIR}/licenses/YSFGateway-SOURCE.txt"
echo "https://github.com/g4klx/YSFClients" >> "${STAGING_DIR}/licenses/YSFGateway-SOURCE.txt"
echo "[01-gateways] YSFGateway built."

echo "[01-gateways] Done. Gateway binaries staged in ${STAGING_DIR}"
