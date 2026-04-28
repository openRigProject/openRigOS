#!/bin/bash
# openrig-rigctld — launch rigctld for the first enabled rig in openrig.json.
# Called exclusively by openrig-rigctld.service.
# Exits 0 (no-op) on hotspot devices; rigctld is only relevant for rigctl
# and console device types. Also exits 0 when no rig is configured.
set -euo pipefail

OPENRIG_JSON="/etc/openrig.json"

# Hotspot devices don't use rigctld.
DEVICE_TYPE=$(jq -r '.openrig.device.type // "unconfigured"' "$OPENRIG_JSON" 2>/dev/null)
if [ "$DEVICE_TYPE" = "hotspot" ]; then
    echo "openrig-rigctld: device type is hotspot — nothing to do."
    exit 0
fi

# Fetch the first enabled rig that has a hamlib_model_id set.
RIG_JSON=$(jq -c '
  first(
    .openrig.radio.rigs[]
    | select(.enabled == true and .hamlib_model_id != null)
  ) // empty
' "$OPENRIG_JSON" 2>/dev/null)

if [ -z "$RIG_JSON" ]; then
    echo "openrig-rigctld: no enabled rig with hamlib_model_id in ${OPENRIG_JSON} — nothing to do."
    exit 0
fi

get() { echo "$RIG_JSON" | jq -r "${1}"; }

MODEL_ID=$(get '.hamlib_model_id')
PORT=$(get '.port     // "/dev/ttyUSB0"')
BAUD=$(get '.baud     // 9600')
DATA_BITS=$(get '.data_bits // 8')
STOP_BITS=$(get '.stop_bits // 1')
PARITY=$(get '.parity   // "none"')
HANDSHAKE=$(get '.handshake // "none"')

# Map openrig.json parity values to hamlib serial_parity strings.
case "$PARITY" in
    even) SERIAL_PARITY="Even" ;;
    odd)  SERIAL_PARITY="Odd"  ;;
    *)    SERIAL_PARITY="None" ;;
esac

# Map openrig.json handshake values to hamlib serial_handshake strings.
case "$HANDSHAKE" in
    hardware) SERIAL_HANDSHAKE="Hardware" ;;
    software) SERIAL_HANDSHAKE="Software" ;;
    *)        SERIAL_HANDSHAKE="None"     ;;
esac

echo "openrig-rigctld: starting — model=${MODEL_ID} port=${PORT} baud=${BAUD}"

exec rigctld \
    --model="${MODEL_ID}" \
    --rig-file="${PORT}" \
    --serial-speed="${BAUD}" \
    -C "serial_data_bits=${DATA_BITS}" \
    -C "serial_stop_bits=${STOP_BITS}" \
    -C "serial_parity=${SERIAL_PARITY}" \
    -C "serial_handshake=${SERIAL_HANDSHAKE}" \
    --listen-addr=0.0.0.0 \
    --port=4532
