#!/bin/bash
# mmdvm-update.sh — Update MMDVM.ini, DMRGateway.ini, YSFGateway.ini
# from /etc/openrig.json values.
# Called by provision.sh, webprovision, and openrig-api after config changes.
# Restarts affected services if running.
set -euo pipefail

OPENRIG_JSON="/etc/openrig.json"
MMDVM_INI="/etc/mmdvm/MMDVM.ini"
DMRGW_INI="/etc/dmrgateway/DMRGateway.ini"
YSFGW_INI="/etc/ysfgateway/YSFGateway.ini"
LOG_TAG="openrig-mmdvm-update"

log() { logger -t "$LOG_TAG" "$*" 2>/dev/null; echo "[mmdvm-update] $*"; }

if [ ! -f "$MMDVM_INI" ]; then
    log "No MMDVM.ini found — skipping."
    exit 0
fi

# Only relevant for hotspot device type
DEVICE_TYPE=$(jq -r '.openrig.device.type // "unconfigured"' "$OPENRIG_JSON" 2>/dev/null)
if [ "$DEVICE_TYPE" != "hotspot" ]; then
    log "Device type is '${DEVICE_TYPE}', not hotspot — skipping."
    exit 0
fi

# Read values from openrig.json
CALLSIGN=$(jq -r '.openrig.operator.callsign // ""' "$OPENRIG_JSON")
COLORCODE=$(jq -r '.openrig.hotspot.dmr.colorcode // 1' "$OPENRIG_JSON")
DMR_ENABLED=$(jq -r 'if .openrig.hotspot.dmr.enabled then "1" else "0" end' "$OPENRIG_JSON")
YSF_ENABLED=$(jq -r 'if .openrig.hotspot.ysf.enabled then "1" else "0" end' "$OPENRIG_JSON")
MODEM_PORT=$(jq -r '.openrig.hotspot.modem.port // "/dev/ttyAMA0"' "$OPENRIG_JSON")
MODEM_SPEED=$(jq -r '.openrig.hotspot.modem.speed // 115200' "$OPENRIG_JSON")

# Modem calibration values (with MMDVM defaults)
RX_OFFSET=$(jq -r '.openrig.hotspot.modem.rx_offset // 0' "$OPENRIG_JSON")
TX_OFFSET=$(jq -r '.openrig.hotspot.modem.tx_offset // 0' "$OPENRIG_JSON")
RX_DC_OFFSET=$(jq -r '.openrig.hotspot.modem.rx_dc_offset // 0' "$OPENRIG_JSON")
TX_DC_OFFSET=$(jq -r '.openrig.hotspot.modem.tx_dc_offset // 0' "$OPENRIG_JSON")
RX_LEVEL=$(jq -r '.openrig.hotspot.modem.rx_level // 50' "$OPENRIG_JSON")
TX_LEVEL=$(jq -r '.openrig.hotspot.modem.tx_level // 50' "$OPENRIG_JSON")
DMR_DELAY=$(jq -r '.openrig.hotspot.modem.dmr_delay // 0' "$OPENRIG_JSON")

# DMR ID: use operator-level if > 0, otherwise fall back to hotspot-level
DMR_ID=$(jq -r '
  (.openrig.operator.dmr_id // 0) as $op |
  (.openrig.hotspot.dmr.dmr_id // 0) as $hs |
  if $op > 0 then $op else $hs end
' "$OPENRIG_JSON")
# Optional 2-digit hotspot suffix (01–99) appended to form a 9-digit hotspot ID.
DMR_ID_SUFFIX=$(jq -r '.openrig.hotspot.dmr.dmr_id_suffix // 0' "$OPENRIG_JSON")
if [ "$DMR_ID_SUFFIX" -gt 0 ] 2>/dev/null; then
    DMR_ID="${DMR_ID}$(printf '%02d' "$DMR_ID_SUFFIX")"
fi
BM_SERVER=$(jq -r '.openrig.hotspot.dmr.bm_server // "uk.brandmeister.network"' "$OPENRIG_JSON")
BM_PASSWORD=$(jq -r '.openrig.hotspot.dmr.bm_password // ""' "$OPENRIG_JSON")
YSF_REFLECTOR=$(jq -r '.openrig.hotspot.ysf.reflector // "AMERICA"' "$OPENRIG_JSON")
YSF_SUFFIX=$(jq -r '.openrig.hotspot.ysf.suffix // ""' "$OPENRIG_JSON")
YSF_DESCRIPTION=$(jq -r '.openrig.hotspot.ysf.description // "openRigOS Hotspot"' "$OPENRIG_JSON")
YSF_WIRESX=$(jq -r 'if .openrig.hotspot.ysf.wiresx_passthrough then "1" else "0" end' "$OPENRIG_JSON")

# Derive YSF suffix from callsign if not explicitly set (last 4 chars, uppercased)
if [ -z "$YSF_SUFFIX" ]; then
    YSF_SUFFIX=$(echo "${CALLSIGN}" | tr '[:lower:]' '[:upper:]' | grep -oP '.{1,4}$' || echo "ND")
fi

# Read frequencies — stored in MHz in openrig.json, convert to Hz for MMDVM.
# rf_frequency is the RX freq (and TX for simplex); tx_frequency is the TX freq for duplex.
# Default to 145.500 MHz simplex if unset.
RX_FREQ=$(jq -r '(.openrig.hotspot.rf_frequency // 145.500) * 1000000 | floor | tostring' "$OPENRIG_JSON")
TX_FREQ_RAW=$(jq -r '.openrig.hotspot.tx_frequency // 0' "$OPENRIG_JSON")
# If tx_frequency is 0 or unset, use rf_frequency (simplex)
if [ "$TX_FREQ_RAW" = "0" ] || [ -z "$TX_FREQ_RAW" ]; then
    TX_FREQ="$RX_FREQ"
else
    TX_FREQ=$(jq -r '(.openrig.hotspot.tx_frequency) * 1000000 | floor | tostring' "$OPENRIG_JSON")
fi

if [ -z "$CALLSIGN" ]; then
    log "No callsign set — leaving config files with placeholders."
    exit 0
fi

# ── MMDVM.ini ─────────────────────────────────────────────────────────────
log "Updating MMDVM.ini: callsign=${CALLSIGN} colorcode=${COLORCODE} DMR=${DMR_ENABLED} YSF=${YSF_ENABLED}"
log "Modem calibration: rx_offset=${RX_OFFSET} tx_offset=${TX_OFFSET} rx_level=${RX_LEVEL} tx_level=${TX_LEVEL} rx_dc_offset=${RX_DC_OFFSET} tx_dc_offset=${TX_DC_OFFSET} dmr_delay=${DMR_DELAY}"

# Apply values using sed (first-run placeholder replacement)
sed -i \
    -e "s|__CALLSIGN__|${CALLSIGN}|g" \
    -e "s|__RX_FREQ__|${RX_FREQ}|g" \
    -e "s|__TX_FREQ__|${TX_FREQ}|g" \
    -e "s|__DMR_COLORCODE__|${COLORCODE}|g" \
    -e "s|__DMR_ENABLE__|${DMR_ENABLED}|g" \
    -e "s|__YSF_ENABLE__|${YSF_ENABLED}|g" \
    -e "s|__MODEM_PORT__|${MODEM_PORT}|g" \
    -e "s|__MODEM_SPEED__|${MODEM_SPEED}|g" \
    "$MMDVM_INI"

# Update already-resolved values (for re-provisioning after first run)
sed -i \
    -e "s|^Callsign=.*|Callsign=${CALLSIGN}|" \
    -e "s|^ColorCode=.*|ColorCode=${COLORCODE}|" \
    "$MMDVM_INI"

# Update DMR ID in [General] section
if [ "$DMR_ID" != "0" ] && [ -n "$DMR_ID" ]; then
    sed -i "s|^Id=.*|Id=${DMR_ID}|" "$MMDVM_INI"
fi

# Section-aware updates for enable flags, modem, frequencies, and calibration
awk -v dmr="$DMR_ENABLED" -v ysf="$YSF_ENABLED" \
    -v port="$MODEM_PORT" -v speed="$MODEM_SPEED" \
    -v rxf="$RX_FREQ" -v txf="$TX_FREQ" \
    -v rx_offset="$RX_OFFSET" -v tx_offset="$TX_OFFSET" \
    -v rx_dc_offset="$RX_DC_OFFSET" -v tx_dc_offset="$TX_DC_OFFSET" \
    -v rx_level="$RX_LEVEL" -v tx_level="$TX_LEVEL" \
    -v dmr_delay="$DMR_DELAY" '
    /^\[/ { section = $0 }
    /^Enable=/ && (section == "[DMR]" || section == "[DMR Network]") { $0 = "Enable=" dmr }
    /^Enable=/ && (section == "[System Fusion]" || section == "[System Fusion Network]") { $0 = "Enable=" ysf }
    /^UARTPort=/ && section == "[Modem]" { $0 = "UARTPort=" port }
    /^UARTSpeed=/ && section == "[Modem]" { $0 = "UARTSpeed=" speed }
    /^RXFrequency=/ && (section == "[Info]" || section == "[Modem]") { $0 = "RXFrequency=" rxf }
    /^TXFrequency=/ && (section == "[Info]" || section == "[Modem]") { $0 = "TXFrequency=" txf }
    /^RXOffset=/ && section == "[Modem]" { $0 = "RXOffset=" rx_offset }
    /^TXOffset=/ && section == "[Modem]" { $0 = "TXOffset=" tx_offset }
    /^RXDCOffset=/ && section == "[Modem]" { $0 = "RXDCOffset=" rx_dc_offset }
    /^TXDCOffset=/ && section == "[Modem]" { $0 = "TXDCOffset=" tx_dc_offset }
    /^RXLevel=/ && section == "[Modem]" { $0 = "RXLevel=" rx_level }
    /^TXLevel=/ && section == "[Modem]" { $0 = "TXLevel=" tx_level }
    /^DMRDelay=/ && section == "[Modem]" { $0 = "DMRDelay=" dmr_delay }
    { print }
' "$MMDVM_INI" > "${MMDVM_INI}.tmp" && mv "${MMDVM_INI}.tmp" "$MMDVM_INI"

chmod 644 "$MMDVM_INI"

# ── DMRGateway.ini ────────────────────────────────────────────────────────
if [ -f "$DMRGW_INI" ]; then
    log "Updating DMRGateway.ini: dmr_id=${DMR_ID} bm_server=${BM_SERVER}"

    # Replace placeholders (first run)
    sed -i \
        -e "s|__DMR_ID__|${DMR_ID}|g" \
        -e "s|__BM_SERVER__|${BM_SERVER}|g" \
        -e "s|__BM_PASSWORD__|${BM_PASSWORD}|g" \
        -e "s|__RX_FREQ__|${RX_FREQ}|g" \
        -e "s|__TX_FREQ__|${TX_FREQ}|g" \
        -e "s|__CALLSIGN__|${CALLSIGN}|g" \
        "$DMRGW_INI"

    # Update already-resolved values (re-provisioning)
    awk -v id="$DMR_ID" -v server="$BM_SERVER" -v pw="$BM_PASSWORD" \
        -v rxf="$RX_FREQ" -v txf="$TX_FREQ" '
        /^\[/ { section = $0 }
        /^Id=/ && section == "[DMR Network 1]" { $0 = "Id=" id }
        /^Address=/ && section == "[DMR Network 1]" { $0 = "Address=" server }
        /^Password=/ && section == "[DMR Network 1]" { $0 = "Password=" pw }
        /^RXFrequency=/ && section == "[Info]" { $0 = "RXFrequency=" rxf }
        /^TXFrequency=/ && section == "[Info]" { $0 = "TXFrequency=" txf }
        { print }
    ' "$DMRGW_INI" > "${DMRGW_INI}.tmp" && mv "${DMRGW_INI}.tmp" "$DMRGW_INI"

    chmod 644 "$DMRGW_INI"
fi

# ── YSFGateway.ini ────────────────────────────────────────────────────────
if [ -f "$YSFGW_INI" ]; then
    log "Updating YSFGateway.ini: callsign=${CALLSIGN} suffix=${YSF_SUFFIX} reflector=${YSF_REFLECTOR}"

    # Replace placeholders (first run)
    sed -i \
        -e "s|__CALLSIGN__|${CALLSIGN}|g" \
        -e "s|__DMR_ID__|${DMR_ID}|g" \
        -e "s|__YSF_SUFFIX__|${YSF_SUFFIX}|g" \
        -e "s|__YSF_DESCRIPTION__|${YSF_DESCRIPTION}|g" \
        -e "s|__YSF_WIRESX_PASSTHROUGH__|${YSF_WIRESX}|g" \
        -e "s|__RX_FREQ__|${RX_FREQ}|g" \
        -e "s|__TX_FREQ__|${TX_FREQ}|g" \
        "$YSFGW_INI"

    # Update already-resolved values (re-provisioning)
    awk -v cs="$CALLSIGN" -v id="$DMR_ID" -v suf="$YSF_SUFFIX" -v ref="$YSF_REFLECTOR" \
        -v desc="$YSF_DESCRIPTION" -v wiresx="$YSF_WIRESX" -v rxf="$RX_FREQ" -v txf="$TX_FREQ" '
        /^\[/ { section = $0 }
        /^Callsign=/ && section == "[General]" { $0 = "Callsign=" cs }
        /^Id=/ && section == "[General]" { $0 = "Id=" id }
        /^Suffix=/ && section == "[General]" { $0 = "Suffix=" suf }
        /^WiresXCommandPassthrough=/ && section == "[General]" { $0 = "WiresXCommandPassthrough=" wiresx }
        /^Name=/ && section == "[Info]" { $0 = "Name=" cs }
        /^Description=/ && section == "[Info]" { $0 = "Description=" desc }
        /^Startup=/ && section == "[Network]" { $0 = "Startup=" ref }
        /^RXFrequency=/ && section == "[Info]" { $0 = "RXFrequency=" rxf }
        /^TXFrequency=/ && section == "[Info]" { $0 = "TXFrequency=" txf }
        { print }
    ' "$YSFGW_INI" > "${YSFGW_INI}.tmp" && mv "${YSFGW_INI}.tmp" "$YSFGW_INI"

    chmod 644 "$YSFGW_INI"
fi

# ── Restart services ──────────────────────────────────────────────────────
if systemctl is-active --quiet openrig-mmdvmhost 2>/dev/null; then
    log "Restarting openrig-mmdvmhost..."
    systemctl restart openrig-mmdvmhost
fi

if systemctl is-active --quiet openrig-dmrgateway 2>/dev/null; then
    log "Restarting openrig-dmrgateway..."
    systemctl restart openrig-dmrgateway
fi

if systemctl is-active --quiet openrig-ysfgateway 2>/dev/null; then
    log "Restarting openrig-ysfgateway..."
    systemctl restart openrig-ysfgateway
fi

log "Done."
