# openRigOS — openrig user profile

# Source .bashrc for interactive shells
[ -f ~/.bashrc ] && source ~/.bashrc

# Run provisioning wizard on first login (provisioned: false in openrig.json)
if [ -f /etc/openrig.json ]; then
    PROVISIONED=$(jq -r '.openrig.device.provisioned // false' /etc/openrig.json 2>/dev/null)
    if [ "$PROVISIONED" = "false" ]; then
        exec sudo /usr/local/lib/openrig/provision.sh
    fi
fi
