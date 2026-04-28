#!/bin/bash
# Stage 0: Compile Go binaries for the target architecture.
# Output goes to /tmp/openrigos-bins/ — NOT into ROOTFS_DIR, because
# 01-bootstrap.sh wipes and recreates ROOTFS_DIR. Stage 07 copies
# the binaries from staging into the final rootfs.
set -euo pipefail

GOARCH="${GOARCH:-amd64}"
STAGING_DIR="/tmp/openrigos-bins"

echo "[00-gobuilds] Compiling Go binaries (GOOS=linux GOARCH=${GOARCH})..."

rm -rf "${STAGING_DIR}"
mkdir -p "${STAGING_DIR}"

build() {
    local name="$1"
    local src_dir="$2"
    echo "[00-gobuilds]   ${name}"
    ( cd "${src_dir}" && CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" \
        go build -ldflags="-s -w" -o "${STAGING_DIR}/${name}" . )
}

build "openrig-provision-web" "/build/src/webprovision"
build "openrig-api"           "/build/src/openrig-api"

echo "[00-gobuilds] Done. Binaries staged in ${STAGING_DIR}"
