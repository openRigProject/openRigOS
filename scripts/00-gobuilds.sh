#!/bin/bash
# Stage 0: Compile Go binaries for the target architecture.
# Output goes to /tmp/openrigos-bins/ — NOT into ROOTFS_DIR, because
# 01-bootstrap.sh wipes and recreates ROOTFS_DIR. Stage 07 copies
# the binaries from staging into the final rootfs.
set -euo pipefail

GOARCH="${GOARCH:-amd64}"
STAGING_DIR="/tmp/openrigos-bins"

echo "[00-gobuilds] Compiling Go binaries (GOOS=linux GOARCH=${GOARCH})..."

mkdir -p "${STAGING_DIR}"
rm -rf "${STAGING_DIR:?}"/*

build() {
    local name="$1"
    local src_dir="$2"
    echo "[00-gobuilds]   ${name}"
    ( cd "${src_dir}" && CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" \
        go build -ldflags="-s -w" -o "${STAGING_DIR}/${name}" . )
}

if [ "${ONLY_API:-0}" = "1" ]; then
    build "openrig-api" "/build/src/openrig-api"
elif [ "${ONLY_WEB:-0}" = "1" ]; then
    build "openrig-provision-web" "/build/src/webprovision"
elif [ "${ONLY_DISPLAY:-0}" = "1" ]; then
    build "openrig-display" "/build/src/openrig-display"
elif [ "${ONLY_WASM:-0}" = "1" ]; then
    echo "[00-gobuilds]   openrig.wasm"
    ( cd /build/src && GOOS=js GOARCH=wasm CGO_ENABLED=0 \
        go build -ldflags="-s -w" -o "${STAGING_DIR}/openrig.wasm" ./wasm/ )
    GOROOT_PATH=$(go env GOROOT)
    cp "${GOROOT_PATH}/lib/wasm/wasm_exec.js" "${STAGING_DIR}/wasm_exec.js"
else
    build "openrig-provision-web" "/build/src/webprovision"
    build "openrig-api"           "/build/src/openrig-api"
    build "openrig-display"       "/build/src/openrig-display"

    # Build WASM client (runs in the browser, not on the device CPU)
    echo "[00-gobuilds]   openrig.wasm"
    ( cd /build/src && GOOS=js GOARCH=wasm CGO_ENABLED=0 \
        go build -ldflags="-s -w" -o "${STAGING_DIR}/openrig.wasm" ./wasm/ )

    # Stage wasm_exec.js from this Go toolchain's GOROOT
    GOROOT_PATH=$(go env GOROOT)
    cp "${GOROOT_PATH}/lib/wasm/wasm_exec.js" "${STAGING_DIR}/wasm_exec.js"
fi

echo "[00-gobuilds] Done. Binaries staged in ${STAGING_DIR}"
