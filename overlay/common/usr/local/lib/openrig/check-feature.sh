#!/bin/bash
# check-feature.sh — Exits 0 if the named feature is enabled, 1 if disabled.
# Used by systemd ExecStartPre to conditionally start services.
# Missing or null keys default to true (enabled).
# Usage: check-feature.sh api_enabled
KEY="$1"
val=$(jq -r ".openrig.management.${KEY} // true" /etc/openrig.json 2>/dev/null)
[ "$val" = "true" ]
