#!/bin/bash
# openRigOS Provisioning Wizard (CLI)
#
# Runs automatically on first login when provisioned=false in /etc/openrig.json.
# Password change is handled by the OS (chage -d 0) before this runs.

OPENRIG_JSON="/etc/openrig.json"
WPA_CONF="/etc/wpa_supplicant/wpa_supplicant.conf"

# ── Helpers ────────────────────────────────────────────────────────────────

red()   { echo -e "\033[0;31m$*\033[0m"; }
green() { echo -e "\033[0;32m$*\033[0m"; }
bold()  { echo -e "\033[1m$*\033[0m"; }
dim()   { echo -e "\033[2m$*\033[0m"; }

jset() {
    local tmp
    tmp=$(mktemp)
    jq "${1} = ${2}" "$OPENRIG_JSON" > "$tmp" && sudo mv "$tmp" "$OPENRIG_JSON"
    sudo chmod 644 "$OPENRIG_JSON"
}

prompt() {
    local label="$1" default="$2"
    if [ -n "$default" ]; then
        read -rp "$(bold "$label") [${default}]: " REPLY
        REPLY="${REPLY:-$default}"
    else
        read -rp "$(bold "$label"): " REPLY
    fi
}

prompt_secret() {
    read -rsp "$(bold "$1"): " REPLY
    echo
}

# ── Steps ──────────────────────────────────────────────────────────────────

step_device_type() {
    echo
    bold "── Device Type ────────────────────────────────────────────"
    echo "  1) Hotspot   — cross-mode digital repeater"
    echo "  2) Rig Control — remote radio operation"
    echo "  3) Console   — station workstation"
    echo

    while true; do
        read -rp "$(bold "  Select type [1-3]"): " choice
        case "$choice" in
            1) DEVICE_TYPE="hotspot"  ; break ;;
            2) DEVICE_TYPE="rigctl"   ; break ;;
            3) DEVICE_TYPE="console"  ; break ;;
            *) red "  Please enter 1, 2, or 3." ;;
        esac
    done

    green "  Device type: ${DEVICE_TYPE}"
}

step_operator() {
    echo
    bold "── Operator ───────────────────────────────────────────────"

    # Callsign
    while true; do
        prompt "Callsign" ""
        CALLSIGN="${REPLY^^}"
        [ -n "$CALLSIGN" ] && break
        red "  Callsign cannot be empty."
    done

    # Name (optional)
    prompt "Name (optional)" ""
    OPERATOR_NAME="$REPLY"

    # Grid square (optional)
    prompt "Grid Square (optional)" ""
    GRID_SQUARE="${REPLY^^}"

    green "  Callsign: ${CALLSIGN}"
}

step_hostname() {
    echo
    bold "── Hostname ───────────────────────────────────────────────"

    # Auto-generate from callsign + device type
    local cs_slug
    cs_slug=$(echo "$CALLSIGN" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9')
    local suggested="${cs_slug}-${DEVICE_TYPE}"

    echo "  Each device on your network must have a unique hostname."
    echo "  mDNS: <hostname>.local"
    echo
    prompt "Hostname" "$suggested"
    HOSTNAME_VAL=$(echo "$REPLY" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9-')

    green "  Hostname: ${HOSTNAME_VAL}  (mDNS: ${HOSTNAME_VAL}.local)"
}

step_location() {
    echo
    bold "── Location ───────────────────────────────────────────────"
    echo "  Country code is used for WiFi regulatory domain and logging."
    echo

    prompt "Country code (ISO 3166-1 alpha-2)" "US"
    COUNTRY="${REPLY^^}"

    echo
    echo "  Common timezones: UTC, America/New_York, America/Chicago,"
    echo "    America/Los_Angeles, Europe/London, Europe/Berlin,"
    echo "    Asia/Tokyo, Australia/Sydney"
    echo "  Full list: ls /usr/share/zoneinfo/"
    echo

    while true; do
        prompt "Timezone" "UTC"
        TIMEZONE="$REPLY"
        if [ -f "/usr/share/zoneinfo/${TIMEZONE}" ]; then
            break
        fi
        red "  '${TIMEZONE}' not found in /usr/share/zoneinfo/. Try again."
    done

    green "  Country: ${COUNTRY}  Timezone: ${TIMEZONE}"
}

step_wifi() {
    echo
    bold "── WiFi Setup ─────────────────────────────────────────────"
    echo "  Leave SSID blank to skip (device will stay in AP mode)."
    echo

    prompt "WiFi SSID" ""
    local ssid="$REPLY"

    if [ -z "$ssid" ]; then
        dim "  WiFi skipped — device will remain in AP mode."
        WIFI_SSID=""
        return
    fi

    prompt_secret "WiFi Password"
    local psk="$REPLY"

    if [ "${#psk}" -lt 8 ]; then
        red "  WiFi password must be at least 8 characters — WiFi not configured."
        WIFI_SSID=""
        return
    fi

    echo
    bold "  Security mode:"
    echo "    1) WPA2 + WPA3 — transition mode, recommended (default)"
    echo "    2) WPA2 only"
    echo "    3) WPA3 only"
    read -rp "  Choice [1]: " sec_choice
    case "${sec_choice:-1}" in
        2) security="wpa2" ; sec_label="WPA2 only" ;;
        3) security="wpa3" ; sec_label="WPA3 only" ;;
        *) security="auto" ; sec_label="WPA2 + WPA3" ;;
    esac

    case "$security" in
        wpa3) network_block="network={\n    ssid=\"${ssid}\"\n    key_mgmt=SAE\n    psk=\"${psk}\"\n    ieee80211w=2\n}" ;;
        wpa2) network_block="network={\n    ssid=\"${ssid}\"\n    key_mgmt=WPA-PSK\n    psk=\"${psk}\"\n}" ;;
        *)    network_block="network={\n    ssid=\"${ssid}\"\n    key_mgmt=WPA-PSK SAE\n    psk=\"${psk}\"\n    ieee80211w=1\n}" ;;
    esac

    printf "country=%s\nctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev\nupdate_config=1\n\n%b\n" \
        "$COUNTRY" "$network_block" | sudo tee "$WPA_CONF" > /dev/null
    sudo chmod 600 "$WPA_CONF"

    WIFI_SSID="$ssid"
    green "  WiFi configured for '${ssid}' (${sec_label})"
}

step_management() {
    echo
    bold "── Management ─────────────────────────────────────────────"
    echo "  These settings control remote management and device discovery."
    echo

    read -rp "$(bold "  Enable remote management API?") [Y/n]: " api_choice
    case "${api_choice,,}" in
        n|no) API_ENABLED=false ;;
        *)    API_ENABLED=true  ;;
    esac

    if [ "$API_ENABLED" = "false" ]; then
        dim "  API disabled. Re-enable via SSH: edit /etc/openrig.json, then:"
        dim "    sudo systemctl start openrig-api"
    else
        green "  Management API: enabled (port 7373)"
    fi

    read -rp "$(bold "  Enable mDNS device discovery?") [Y/n]: " mdns_choice
    case "${mdns_choice,,}" in
        n|no) MDNS_ENABLED=false ;;
        *)    MDNS_ENABLED=true  ;;
    esac

    if [ "$MDNS_ENABLED" = "false" ]; then
        dim "  mDNS disabled — device will not advertise openRig services."
        dim "  SSH discovery (_ssh._tcp) remains active."
    else
        green "  mDNS discovery: enabled"
    fi
}

step_dmrid() {
    echo
    bold "── DMR ID ──────────────────────────────────────────────────"
    echo "  Your 7-digit DMR ID from radioid.net (required for BrandMeister)."
    echo "  Leave blank to skip (can be set later in the web UI)."
    echo

    while true; do
        prompt "DMR ID (7 digits, or blank to skip)" ""
        DMR_ID="$REPLY"
        if [ -z "$DMR_ID" ]; then
            DMR_ID=0
            break
        fi
        if [[ "$DMR_ID" =~ ^[0-9]{7}$ ]] && [ "$DMR_ID" -ge 1000000 ] && [ "$DMR_ID" -le 9999999 ]; then
            break
        fi
        red "  DMR ID must be exactly 7 digits (1000000–9999999)."
    done

    [ "$DMR_ID" != "0" ] && green "  DMR ID: ${DMR_ID}" || dim "  DMR ID skipped."
}

step_apply() {
    echo
    bold "── Applying Configuration ─────────────────────────────────"

    # Update openrig.json
    jset '.openrig.device.hostname'            "\"$HOSTNAME_VAL\""
    jset '.openrig.device.type'               "\"$DEVICE_TYPE\""
    jset '.openrig.device.timezone'           "\"$TIMEZONE\""
    jset '.openrig.operator.callsign'         "\"$CALLSIGN\""
    jset '.openrig.operator.name'             "\"$OPERATOR_NAME\""
    jset '.openrig.operator.grid_square'      "\"$GRID_SQUARE\""
    jset '.openrig.operator.country'          "\"$COUNTRY\""
    jset '.openrig.network.wifi.country'      "\"$COUNTRY\""
    jset '.openrig.management.api_enabled'   "${API_ENABLED:-true}"
    jset '.openrig.management.mdns_enabled'  "${MDNS_ENABLED:-true}"
    jset '.openrig.device.provisioned'        'true'

    # Timezone
    sudo timedatectl set-timezone "$TIMEZONE" 2>/dev/null && \
        green "  Timezone set to ${TIMEZONE}" || \
        red "  Could not set timezone — set manually with: timedatectl set-timezone"

    # Hostname
    sudo hostnamectl set-hostname "$HOSTNAME_VAL"
    sudo sed -i "s/127.0.1.1.*/127.0.1.1   ${HOSTNAME_VAL}/" /etc/hosts
    sudo systemctl restart avahi-daemon 2>/dev/null || true
    green "  Hostname: ${HOSTNAME_VAL}  (${HOSTNAME_VAL}.local on mDNS)"

    # Avahi DNS-SD — write service file based on mdns_enabled setting.
    # Avahi detects the change via inotify; no restart needed.
    sudo /usr/local/lib/openrig/update-mdns.sh
    if [ "${MDNS_ENABLED:-true}" = "true" ]; then
        green "  mDNS: _openrig._tcp advertised as ${HOSTNAME_VAL}.local (${DEVICE_TYPE}, ${CALLSIGN})"
    else
        dim "  mDNS: openRig services hidden (SSH-only discovery)"
    fi

    # WiFi
    if [ -n "${WIFI_SSID:-}" ]; then
        dim "  Applying WiFi..."
        sudo systemctl restart openrig-wifi
    fi

    # Hotspot-specific config: DMR ID and MMDVM update
    if [ "$DEVICE_TYPE" = "hotspot" ]; then
        if [ "${DMR_ID:-0}" != "0" ]; then
            jset '.openrig.hotspot.dmr.dmr_id' "$DMR_ID"
        fi
        dim "  Updating MMDVM config..."
        sudo /usr/local/lib/openrig/mmdvm-update.sh
        green "  MMDVMHost configured for ${CALLSIGN}"
    fi
}

# ── Main ───────────────────────────────────────────────────────────────────

clear
echo
bold "╔══════════════════════════════════════════╗"
bold "║       openRigOS First-Time Setup         ║"
bold "╚══════════════════════════════════════════╝"
echo
echo "  Your password has already been changed."
echo "  Let's finish setting up this device."

step_device_type
step_operator
step_hostname
step_location
step_wifi
[ "$DEVICE_TYPE" = "hotspot" ] && step_dmrid
step_management
step_apply

echo
bold "── Done ────────────────────────────────────────────────────"
green "  Device provisioned successfully."
echo
echo "  Callsign  : ${CALLSIGN}"
echo "  Device    : ${DEVICE_TYPE}"
echo "  Hostname  : ${HOSTNAME_VAL}.local"
echo "  Timezone  : ${TIMEZONE}"
[ -n "${WIFI_SSID:-}" ] && echo "  WiFi      : ${WIFI_SSID}"
[ "$DEVICE_TYPE" = "hotspot" ] && [ "${DMR_ID:-0}" != "0" ] && echo "  DMR ID    : ${DMR_ID}"
echo
dim "  To reconfigure at any time: sudo openrig-provision"
echo
