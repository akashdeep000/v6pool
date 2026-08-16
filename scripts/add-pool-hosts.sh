#!/usr/bin/env bash
# add-pool-hosts.sh - switch v6pool to ADDRESS-LIST rotation mode.
#
# Works with any provider: pass the IPv6 addresses the provider routes to this
# host, whatever their origin:
#   - routed /128 addresses added to the interface (Hetzner, OVH, Vultr, ...)
#   - IPv6 objects allocated to the VNIC (Oracle Cloud, up to 32 per VNIC)
#   - DHCPv6/SLAAC-assigned addresses, manual `ip addr add`, ...
#
# Usage:
#   sudo ./scripts/add-pool-hosts.sh 2001:db8:1:2:0:aaaa:bbbb:1 2001:db8:1:2:0:aaaa:bbbb:2 2001:db8:1:2:0:aaaa:bbbb:3
#
# Writes a pool_hosts block into /etc/v6pool/config.yaml (removing
# pool_prefix / pool_bits / source_iface / fixed_source) and restarts the
# service. The same result can be achieved from scratch with:
#   PROXY_POOL_HOSTS="addr1 addr2 ..." sudo ./install.sh
#
# Requires the local-route + ip_nonlocal_bind setup, which install.sh performs
# automatically; re-run the installer once if you haven't.
set -euo pipefail

conf=${V6POOL_CONF:-/etc/v6pool/config.yaml}
if [ $# -lt 2 ]; then
  echo "usage: $0 <addr1> <addr2> [...]" >&2
  exit 1
fi

sudo_cmd=()
[ "$(id -u)" -eq 0 ] || sudo_cmd=(sudo)

[ -f "$conf" ] || { echo "config not found: $conf" >&2; exit 1; }
"${sudo_cmd[@]}" cp -f "$conf" "$conf.bak-$(date +%s)"

"${sudo_cmd[@]}" sed -i \
  -e '/^pool_prefix:/d' \
  -e '/^pool_bits:/d' \
  -e '/^source_iface:/d' \
  -e '/^fixed_source:/d' \
  -e '/^pool_hosts:/,/^[a-z]/d' \
  "$conf"

{
  echo "pool_hosts:"
  for a in "$@"; do
    echo "  - \"$a\""
  done
} | "${sudo_cmd[@]}" tee -a "$conf" >/dev/null

"${sudo_cmd[@]}" systemctl restart v6pool
sleep 1
"${sudo_cmd[@]}" systemctl is-active v6pool >/dev/null || {
  echo "service failed to start - check: journalctl -u v6pool" >&2
  exit 1
}
echo "v6pool now rotating over: $*"