#!/usr/bin/env bash
# update-prefix.sh - point v6pool's pool at the current IPv6 /64.
#
# For setups where the provider hands out a *dynamic* /64 (e.g. a phone USB /
# Wi-Fi tether using SLAAC), the prefix can change on every reconnect. v6pool
# generates source addresses from the static `pool_prefix` in its config, so
# the config must track the live prefix.
#
# This script:
#   1. finds the interface that owns a global IPv6 /64 (default: the one the
#      default route currently uses; override with IFACE=...)
#   2. rewrites `pool_prefix:` in /etc/v6pool/config.yaml to that /64
#   3. restarts the v6pool service (only when the prefix actually changed)
#
# It is idempotent: safe to run on a timer so a roaming /64 is tracked
# automatically (or rely on the built-in auto_pool mode instead, which tracks
# the live prefix per-request and needs no helper script at all). Run manually
# after tether reconnects:
#   sudo ./scripts/update-prefix.sh                # auto-detect iface + prefix
#   IFACE=enp0s20f0u2 sudo ./scripts/update-prefix.sh
set -euo pipefail

conf=${V6POOL_CONF:-/etc/v6pool/config.yaml}
service=${V6POOL_SERVICE:-v6pool}
sudo_cmd=()
[ "$(id -u)" -eq 0 ] || sudo_cmd=(sudo)

iface=${IFACE:-}
if [ -z "$iface" ]; then
  iface=$("${sudo_cmd[@]}" ip -6 route show default 2>/dev/null |
    awk '/default/{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')
fi
[ -n "$iface" ] || { echo "could not determine default IPv6 interface" >&2; exit 1; }

[ -f "$conf" ] || { echo "config not found: $conf" >&2; exit 1; }

# Not in pool_prefix mode: pool_hosts / fixed_source / source_iface modes
# manage their own sources (source_iface already tracks the live prefix by
# itself), so there is nothing to keep in sync here.
if grep -Eq '^[[:space:]]*(pool_hosts|fixed_source|source_iface):' "$conf"; then
  echo "config is not in pool_prefix mode; nothing to do"
  exit 0
fi

# First global unicast address on the interface, e.g. 2401:4900:b770:8749:4180:...:312f/64
addr=$("${sudo_cmd[@]}" ip -6 addr show dev "$iface" scope global 2>/dev/null |
  awk '/inet6/{print $2; exit}')
if [ -z "$addr" ]; then
  echo "no global IPv6 address on $iface" >&2
  exit 1
fi

len=${addr##*/}
host=${addr%%/*}
if [ "$len" != "64" ]; then
  echo "note: $iface has a global /$len (not /64) - $host/$len" >&2
fi
# Normalise to the first four hextets + "::" -> 2401:4900:b770:8749::
base=$(printf '%s' "$host" | cut -d: -f1-4)"::"
p64="$base/$len"
echo "interface $iface -> pool_prefix \"$p64\""

current=$("${sudo_cmd[@]}" sed -n 's/^pool_prefix:[[:space:]]*"\([^"]*\)".*/\1/p' "$conf" | head -1)
if [ "$current" = "$p64" ]; then
  echo "pool_prefix already up to date (no restart needed)"
  exit 0
fi

"${sudo_cmd[@]}" cp -f "$conf" "$conf.bak-$(date +%s)"
if grep -q '^pool_prefix:' "$conf"; then
  "${sudo_cmd[@]}" sed -i "s|^pool_prefix:.*|pool_prefix: \"$p64\"|" "$conf"
else
  printf 'pool_prefix: "%s"\n' "$p64" | "${sudo_cmd[@]}" tee -a "$conf" >/dev/null
fi
echo "updated pool_prefix -> $p64"
"${sudo_cmd[@]}" systemctl restart "$service"
sleep 1
"${sudo_cmd[@]}" systemctl is-active "$service" >/dev/null || {
  echo "service failed to restart - check: journalctl -u $service" >&2
  exit 1
}
echo "restarted $service"
