#!/bin/bash
# update-mdns.sh — Write avahi service file based on mdns_enabled setting.
# If mdns_enabled=true: full openRig service advertisements.
# If mdns_enabled=false: SSH-only (keep device SSH-discoverable).
# Called by provisioning and can be run manually.
set -euo pipefail

OPENRIG_JSON="/etc/openrig.json"
AVAHI_SERVICE="/etc/avahi/services/openrig.service"

MDNS_ENABLED=$(jq -r '.openrig.management.mdns_enabled // true' "$OPENRIG_JSON" 2>/dev/null)

if [ "$MDNS_ENABLED" = "false" ]; then
    cat > "$AVAHI_SERVICE" <<'EOF'
<?xml version="1.0" standalone='no'?>
<!DOCTYPE service-group SYSTEM "avahi-service.dtd">
<service-group>
  <name replace-wildcards="yes">%h</name>
  <service>
    <type>_ssh._tcp</type>
    <port>22</port>
  </service>
  <service>
    <type>_http._tcp</type>
    <port>80</port>
  </service>
</service-group>
EOF
else
    # Full service file — read identity from config
    CALLSIGN=$(jq -r '.openrig.operator.callsign // ""' "$OPENRIG_JSON")
    DEVICE_TYPE=$(jq -r '.openrig.device.type // "unconfigured"' "$OPENRIG_JSON")
    VERSION=$(jq -r '.openrig.version // "0.1.0"' "$OPENRIG_JSON")

    {
        cat <<AVAHI_BASE
<?xml version="1.0" standalone='no'?>
<!DOCTYPE service-group SYSTEM "avahi-service.dtd">
<service-group>
  <name replace-wildcards="yes">openRig %h</name>
  <service>
    <type>_openrig._tcp</type>
    <port>7373</port>
    <txt-record>provisioned=true</txt-record>
    <txt-record>type=${DEVICE_TYPE}</txt-record>
    <txt-record>callsign=${CALLSIGN}</txt-record>
    <txt-record>version=${VERSION}</txt-record>
  </service>
  <service>
    <type>_ssh._tcp</type>
    <port>22</port>
  </service>
  <service>
    <type>_http._tcp</type>
    <port>80</port>
  </service>
AVAHI_BASE

        if [ "$DEVICE_TYPE" = "rigctl" ] || [ "$DEVICE_TYPE" = "console" ]; then
            cat <<AVAHI_RIGCTLD
  <service>
    <type>_rigctld._tcp</type>
    <port>4532</port>
    <txt-record>callsign=${CALLSIGN}</txt-record>
  </service>
AVAHI_RIGCTLD
        fi

        echo "</service-group>"
    } > "$AVAHI_SERVICE"
fi

chmod 644 "$AVAHI_SERVICE"
