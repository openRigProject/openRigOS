#!/bin/bash
# Download and normalize YSF/FCS host files.
set -euo pipefail

JQ_FILTER='{reflectors:[.reflectors[]|{designator,country,name,use_xx_prefix,description:(.description//""),user_count:(.user_count//"000"),port:(.port//42000),ipv4:(.ipv4//null),ipv6:(.ipv6//null)}]}'

_ysf_tmp=$(mktemp)
if curl --fail --silent -S -L \
    -A "openRig - KC1YGY" \
    https://hostfiles.refcheck.radio/YSFHosts.json \
  | jq "$JQ_FILTER" \
  > "${_ysf_tmp}" && [ -s "${_ysf_tmp}" ]; then
    mv "${_ysf_tmp}" /usr/local/etc/YSFHosts.json
    chown openrig:openrig /usr/local/etc/YSFHosts.json
    chmod 644 /usr/local/etc/YSFHosts.json
else
    rm -f "${_ysf_tmp}"
    echo "WARNING: YSFHosts.json download failed — existing file preserved" >&2
fi

curl --fail --silent -S -L \
    -o /usr/local/etc/FCSHosts.txt \
    https://www.pistar.uk/downloads/FCS_Hosts.txt
