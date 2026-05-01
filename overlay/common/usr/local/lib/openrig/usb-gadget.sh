#!/bin/bash
# openRigOS USB Gadget Manager
#
# Reads /etc/openrig.json at boot. If usb_gadget.enabled is true,
# loads the gadget ethernet kernel modules and brings up the interface
# with the configured static IP.
#
# To enable:  sudo jq '.openrig.network.usb_gadget.enabled = true' \
#               /etc/openrig.json | sudo tee /etc/openrig.json.tmp \
#               && sudo mv /etc/openrig.json.tmp /etc/openrig.json
# Then reboot (or: sudo systemctl restart openrig-usb-gadget)

set -euo pipefail

OPENRIG_JSON="/etc/openrig.json"
LOG_TAG="openrig-usb-gadget"

log()  { logger -t "$LOG_TAG" "$*"; echo "[openrig-usb-gadget] $*"; }

jval() {
    jq -r "${1} // ${2}" "$OPENRIG_JSON" 2>/dev/null || echo "${2//\"/}"
}

ENABLED=$(jval '.openrig.network.usb_gadget.enabled' 'false')
IP=$(jval      '.openrig.network.usb_gadget.ip'      '"10.55.0.1"')
NETMASK=$(jval '.openrig.network.usb_gadget.netmask' '"255.255.255.0"')

if [ "$ENABLED" != "true" ]; then
    log "USB gadget disabled in /etc/openrig.json — skipping."
    exit 0
fi

log "Enabling USB gadget ethernet (usb0 → ${IP})..."

# Load the OTG controller driver then the gadget ethernet function
modprobe dwc2      || { log "WARNING: dwc2 module not available on this hardware"; exit 0; }
modprobe g_ether   || { log "WARNING: g_ether module not available"; exit 0; }

# Give the kernel a moment to enumerate the interface
WAIT=0
while [ $WAIT -lt 10 ]; do
    ip link show usb0 &>/dev/null && break
    sleep 1
    WAIT=$(( WAIT + 1 ))
done

if ! ip link show usb0 &>/dev/null; then
    log "usb0 interface did not appear — hardware may not support USB gadget mode."
    exit 0
fi

# Assign static IP
ip link set usb0 up
ip addr flush dev usb0
ip addr add "${IP}/${NETMASK}" dev usb0

# Serve DHCP on the USB interface so the host gets an address automatically
USB_DNSMASQ_CONF="/run/openrig/dnsmasq-usb.conf"
USB_DNSMASQ_PID="/run/openrig/dnsmasq-usb.pid"
mkdir -p /run/openrig

# Calculate a host address in the same /24 subnet
USB_DHCP_START="${IP%.*}.2"
USB_DHCP_END="${IP%.*}.10"

cat > "$USB_DNSMASQ_CONF" <<EOF
interface=usb0
bind-interfaces
port=0
dhcp-range=${USB_DHCP_START},${USB_DHCP_END},255.255.255.0,12h
no-resolv
no-poll
EOF

# Kill any stale dnsmasq instance for this interface
pkill -F "$USB_DNSMASQ_PID" 2>/dev/null || true
dnsmasq --conf-file="$USB_DNSMASQ_CONF" --pid-file="$USB_DNSMASQ_PID" \
    || log "WARNING: dnsmasq for usb0 failed to start"

log "usb0 up — connect via USB and SSH to ${IP} as openrig"
