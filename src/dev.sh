#!/bin/bash
# Run the webprovision + openrig-api locally for UI development.
#
# Usage:
#   cd openRigOS/src
#   ./dev.sh
#
# webprovision → http://localhost:8080   (provisioning wizard or /management)
# openrig-api  → http://localhost:7373   (REST API consumed by management page)
#
# Both services share ./openrig.json in this directory.
# A seed config is auto-created on first run.
# All system calls (chpasswd, systemctl, scripts, etc.) are stubbed.

set -euo pipefail
cd "$(dirname "$0")"

cleanup() {
    echo ""
    echo "Stopping dev servers..."
    kill "$API_PID" 2>/dev/null || true
    wait "$API_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "Building WASM client..."
GOOS=js GOARCH=wasm go build -o /tmp/openrig.wasm ./wasm/

echo "Building openrig-api..."
(cd openrig-api && go build -o openrig-api .)

echo "Building webprovision..."
(cd webprovision && go build -o webprovision .)

echo ""
echo "Starting openrig-api on :7373 ..."
./openrig-api/openrig-api -dev &
API_PID=$!

sleep 0.3

echo "Starting webprovision on :8080 ..."
echo ""
echo "  Provisioning wizard : http://localhost:8080"
echo "  Management page     : http://localhost:8080/management"
echo "  REST API            : http://localhost:7373/api/status"
echo ""
echo "Press Ctrl+C to stop."
echo ""

./webprovision/webprovision -dev
