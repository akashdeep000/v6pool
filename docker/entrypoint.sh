#!/bin/sh
# v6pool container entrypoint.
#
# Zero-config bootstrap: when no config file exists, a minimal one is
# generated from the V6POOL_* environment variables (see docker-compose.yml
# for the supported set). Otherwise the mounted config is used as-is.
#
# The final exec replaces the shell with v6pool, so signals (SIGTERM/SIGINT)
# from docker stop reach the proxy directly.

set -e

CONFIG="${V6POOL_CONFIG:-/etc/v6pool/config.yaml}"

if [ ! -f "$CONFIG" ]; then
  if [ -n "${V6POOL_POOL_PREFIX:-}" ] || [ -n "${V6POOL_POOL_HOSTS:-}" ] || \
     [ -n "${V6POOL_AUTO_POOL:-}" ] || [ -n "${V6POOL_FIXED_SOURCE:-}" ]; then
    {
      echo "http_listen: \":${V6POOL_HTTP_PORT:-3128}\""
      echo "socks5_listen: \":${V6POOL_SOCKS_PORT:-1080}\""
      echo "stats_listen: \"${V6POOL_STATS_LISTEN:-127.0.0.1:${V6POOL_STATS_PORT:-9090}}\""
      [ -n "${V6POOL_STATS_TOKEN:-}" ] && echo "stats_token: \"$V6POOL_STATS_TOKEN\""
      [ "${V6POOL_DISABLE_HTTP:-false}" = "true" ] && echo "enable_http: false"
      [ "${V6POOL_DISABLE_SOCKS5:-false}" = "true" ] && echo "enable_socks5: false"
      [ "${V6POOL_DISABLE_STATS:-false}" = "true" ] && echo "enable_stats: false"
      if [ -n "${V6POOL_POOL_PREFIX:-}" ]; then
        echo "pool_prefix: \"$V6POOL_POOL_PREFIX\""
        echo "pool_bits: ${V6POOL_POOL_BITS:-64}"
      fi
      if [ -n "${V6POOL_POOL_HOSTS:-}" ]; then
        echo "pool_hosts:"
        for h in $V6POOL_POOL_HOSTS; do
          echo "  - \"$h\""
        done
      fi
      [ -n "${V6POOL_SOURCE_IFACE:-}" ] && echo "source_iface: \"$V6POOL_SOURCE_IFACE\""
      [ "${V6POOL_AUTO_POOL:-false}" = "true" ] && echo "auto_pool: true"
      [ -n "${V6POOL_FIXED_SOURCE:-}" ] && echo "fixed_source: \"$V6POOL_FIXED_SOURCE\""
      [ -n "${V6POOL_CLAIM_IFACE:-}" ] && echo "claim_iface: \"$V6POOL_CLAIM_IFACE\""
      echo "accounts:"
      echo "  - name: primary"
      echo "    username: \"${V6POOL_USER:?V6POOL_USER is required when generating a config}\""
      echo "    password: \"${V6POOL_PASS:?V6POOL_PASS is required when generating a config}\""
    } > "$CONFIG"
  else
    echo "v6pool: no config at $CONFIG" >&2
    echo "v6pool: mount one (e.g. -v \$PWD/config.yaml:$CONFIG:ro) or set" >&2
    echo "v6pool: V6POOL_POOL_PREFIX (or V6POOL_AUTO_POOL=true) with" >&2
    echo "v6pool: V6POOL_USER/V6POOL_PASS to generate a config from env" >&2
    exit 1
  fi
fi

exec /app/app -config "$CONFIG"
