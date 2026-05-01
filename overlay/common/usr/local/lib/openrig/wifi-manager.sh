#!/bin/bash
# openRigOS WiFi Manager
#
# Behaviour:
#   1. No WiFi hardware found  → exit (nothing to do)
#   2. WiFi not configured     → start AP immediately
#   3. WiFi configured         → try to connect; if no IP within
#                                fallback_ap.timeout_minutes → start AP
#                                (if fallback_ap.enabled)
#
# AP SSID: <ssid_prefix><last-4-of-MAC>  e.g. "openRig-A1B2"
# AP IP:   192.168.73.1  (73 = ham radio convention)

set -euo pipefail

OPENRIG_JSON="/etc/openrig.json"
LOG_TAG="openrig-wifi"
HOSTAPD_CONF="/run/openrig/hostapd.conf"
DNSMASQ_CONF="/run/openrig/dnsmasq-ap.conf"
DNSMASQ_PID="/run/openrig/dnsmasq-ap.pid"

log()  { logger -t "$LOG_TAG" "$*"; echo "[openrig-wifi] $*"; }
fatal(){ log "FATAL: $*"; exit 1; }

# ---------------------------------------------------------------------------
# Read config values via jq with fallbacks
# ---------------------------------------------------------------------------
jval() {
    jq -r "${1} // ${2}" "$OPENRIG_JSON" 2>/dev/null || echo "${2//\"/}"
}

WIFI_COUNTRY=$(jval '.openrig.network.wifi.country'                    '"US"')
FALLBACK_ENABLED=$(jval '.openrig.network.wifi.fallback_ap.enabled'   'true')
FALLBACK_MINUTES=$(jval '.openrig.network.wifi.fallback_ap.timeout_minutes' '2')
AP_PREFIX=$(jval   '.openrig.network.access_point.ssid_prefix'        '"openRig-"')
AP_CHANNEL=$(jval  '.openrig.network.access_point.channel'            '6')
AP_IP=$(jval       '.openrig.network.access_point.ip'                 '"192.168.73.1"')
AP_NETMASK=$(jval  '.openrig.network.access_point.netmask'            '"255.255.255.0"')
AP_DHCP_START=$(jval '.openrig.network.access_point.dhcp_range_start' '"192.168.73.100"')
AP_DHCP_END=$(jval   '.openrig.network.access_point.dhcp_range_end'   '"192.168.73.200"')
AP_DHCP_LEASE=$(jval '.openrig.network.access_point.dhcp_lease_hours' '12')

FALLBACK_SECONDS=$(( FALLBACK_MINUTES * 60 ))

# ---------------------------------------------------------------------------
# Find the first wireless interface
# ---------------------------------------------------------------------------
find_wifi_interface() {
    for dir in /sys/class/net/*/wireless; do
        [ -d "$dir" ] && basename "$(dirname "$dir")" && return 0
    done
    return 1
}

# ---------------------------------------------------------------------------
# WiFi is "configured" if wpa_supplicant.conf has at least one network
# block with a psk or password entry
# ---------------------------------------------------------------------------
wifi_is_configured() {
    grep -qE '^\s*(psk|password)\s*=' /etc/wpa_supplicant/wpa_supplicant.conf 2>/dev/null
}

# ---------------------------------------------------------------------------
# Check whether the interface has obtained an IPv4 address
# ---------------------------------------------------------------------------
has_ip() {
    ip -4 addr show "$WIFI_IF" 2>/dev/null | grep -q 'inet '
}

# ---------------------------------------------------------------------------
# Bring up wpa_supplicant for station (client) mode
# ---------------------------------------------------------------------------
start_station() {
    log "Starting WiFi station mode on ${WIFI_IF}..."
    rfkill unblock wifi 2>/dev/null || true
    iw reg set "$WIFI_COUNTRY" 2>/dev/null || true
    # Bring interface down first so wpa_supplicant can set managed mode cleanly
    # (important when restarting after AP/hostapd mode)
    ip link set "$WIFI_IF" down 2>/dev/null || true
    ip addr flush dev "$WIFI_IF" 2>/dev/null || true
    ip link set "$WIFI_IF" up 2>/dev/null || true
    # Kill any stale wpa_supplicant before starting fresh
    pkill -F "/run/wpa_supplicant-${WIFI_IF}.pid" 2>/dev/null || true
    wpa_supplicant -B -i "$WIFI_IF" \
        -c /etc/wpa_supplicant/wpa_supplicant.conf \
        -P "/run/wpa_supplicant-${WIFI_IF}.pid" \
        || log "WARNING: wpa_supplicant failed to start"

    # Request DHCP lease — wpa_supplicant handles association only
    dhclient -nw "$WIFI_IF" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Start AP mode:  configure interface → write hostapd.conf → write
# dnsmasq conf → start both daemons
# ---------------------------------------------------------------------------
start_ap() {
    log "Starting Access Point on ${WIFI_IF} (SSID: ${AP_SSID})..."

    # Stop station mode if it was running
    systemctl stop "wpa_supplicant@${WIFI_IF}" 2>/dev/null || true
    pkill -F "/run/wpa_supplicant-${WIFI_IF}.pid" 2>/dev/null || true
    sleep 1

    mkdir -p /run/openrig

    # Unblock WiFi radio (some devices start soft-blocked)
    rfkill unblock wifi 2>/dev/null || true

    # Take interface down cleanly before hostapd takes ownership
    ip link set "$WIFI_IF" down 2>/dev/null || true
    ip addr flush dev "$WIFI_IF" 2>/dev/null || true
    sleep 1

    # Write hostapd config — country_code lets hostapd handle regulatory itself
    cat > "$HOSTAPD_CONF" <<EOF
interface=${WIFI_IF}
driver=nl80211
ssid=${AP_SSID}
hw_mode=g
channel=${AP_CHANNEL}
country_code=${WIFI_COUNTRY}
ieee80211d=1
wmm_enabled=0
macaddr_acl=0
auth_algs=1
ignore_broadcast_ssid=0
# Open network — connect and configure via web UI or SSH
# Add wpa_passphrase here to password-protect the AP
EOF

    # Check if provisioning is needed — if so, enable captive portal DNS
    PROVISIONED=$(jq -r '.openrig.device.provisioned // false' "$OPENRIG_JSON" 2>/dev/null || echo "false")

    # Write dnsmasq config (AP-only instance)
    cat > "$DNSMASQ_CONF" <<EOF
interface=${WIFI_IF}
bind-interfaces
dhcp-range=${AP_DHCP_START},${AP_DHCP_END},${AP_DHCP_LEASE}h
dhcp-option=3,${AP_IP}
dhcp-option=6,${AP_IP}
no-resolv
no-poll
EOF

    # Captive portal: redirect all DNS to this device so any browser
    # request lands on the provisioning web UI
    if [ "$PROVISIONED" = "false" ]; then
        echo "address=/#/${AP_IP}" >> "$DNSMASQ_CONF"
        log "Captive portal DNS active — all HTTP traffic will redirect to ${AP_IP}"
    fi

    # Start hostapd — it brings the interface up in AP mode
    hostapd -B "$HOSTAPD_CONF" -f /tmp/hostapd.log || fatal "hostapd failed to start (see /tmp/hostapd.log)"
    sleep 1

    # Assign static IP after hostapd has configured the interface
    ip addr flush dev "$WIFI_IF"
    ip addr add "${AP_IP}/${AP_NETMASK}" dev "$WIFI_IF"
    ip link set "$WIFI_IF" up

    # Start dedicated dnsmasq instance for AP DHCP
    dnsmasq --conf-file="$DNSMASQ_CONF" --pid-file="$DNSMASQ_PID" \
        || fatal "dnsmasq failed to start"

    log "AP active — SSID: ${AP_SSID}  IP: ${AP_IP}"
    log "Connect to '${AP_SSID}' and SSH to ${AP_IP} as openrig"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
WIFI_IF=$(find_wifi_interface) || { log "No WiFi hardware found, exiting."; exit 0; }
log "WiFi interface: ${WIFI_IF}"

# Generate AP SSID from last 4 hex chars of MAC (unique per device)
MAC=$(cat "/sys/class/net/${WIFI_IF}/address" 2>/dev/null | tr -d ':')
AP_SSID="${AP_PREFIX}${MAC: -4}"
AP_SSID="${AP_SSID^^}"   # uppercase

if ! wifi_is_configured; then
    log "WiFi not configured — starting AP immediately."
    start_ap
    exit 0
fi

# WiFi is configured — attempt station connection
start_station

log "Waiting up to ${FALLBACK_MINUTES} minute(s) for WiFi connection..."
ELAPSED=0
POLL=10
while [ "$ELAPSED" -lt "$FALLBACK_SECONDS" ]; do
    if has_ip; then
        IP=$(ip -4 addr show "$WIFI_IF" | awk '/inet /{print $2}')
        log "WiFi connected — ${WIFI_IF} got ${IP}"
        # Kick timesyncd now that we have a network connection
        systemctl restart systemd-timesyncd 2>/dev/null || true
        exit 0
    fi
    sleep "$POLL"
    ELAPSED=$(( ELAPSED + POLL ))
done

# Timed out
if [ "$FALLBACK_ENABLED" = "true" ]; then
    log "WiFi connection timed out after ${FALLBACK_MINUTES} minute(s) — falling back to AP."
    start_ap
else
    log "WiFi connection timed out — fallback AP disabled, giving up."
fi
