#!/usr/bin/env bash
# docker-run.sh - run v6pool as a container, zero-config.
#
# Uses the environment the same way as docker-compose.yml (V6POOL_* vars,
# see docker/entrypoint.sh) or a mounted config:
#
#   V6POOL_USER=me V6POOL_PASS=secret V6POOL_POOL_PREFIX=2001:db8:1:2:: \
#     V6POOL_STATS_TOKEN=token ./docker-run.sh
#
#   V6POOL_CONFIG=/path/to/config.yaml ./docker-run.sh
#
# Traefik mode (no host ports, no collisions): put the container on your
# Traefik docker network and route its HTTP proxy through a TCP router:
#
#   V6POOL_TRAEFIK_NETWORK=traefik V6POOL_SOCKS_PORT="" ./docker-run.sh
#
# (Traefik needs a dedicated TCP entrypoint, e.g. `proxy` on :3128 — raw
# proxy traffic carries no hostname/SNI. Claim mode is unavailable here;
# leave V6POOL_TRAEFIK_NETWORK unset for --network host + NET_ADMIN.)
#
# Subcommands: run (default), stop, logs, restart
set -euo pipefail

IMAGE="${V6POOL_IMAGE:-ghcr.io/akashdeep000/v6pool:latest}"
NAME="${V6POOL_NAME:-v6pool}"
CONFIG="${V6POOL_CONFIG:-}"
# V6POOL_TRAEFIK_NETWORK=traefik runs on a docker network with Traefik TCP
# routing and publishes no host ports (see docker-compose.yml profile
# "traefik"). Anything else keeps --network host (needed for claim mode).
TRAEFIK_NETWORK="${V6POOL_TRAEFIK_NETWORK:-}"

usage() {
  echo "usage: $(basename "$0") [run|stop|restart|logs]" >&2
  exit 1
}

cmd="${1:-run}"

case "$cmd" in
  run)
    args=(
      -d
      --name "$NAME"
      --restart unless-stopped
    )
    if [ -n "$TRAEFIK_NETWORK" ]; then
      # Bridged + Traefik TCP router: no host ports at all. Works with
      # auto_pool / pool_prefix / source_iface / fixed_source; claiming needs
      # host networking (set V6POOL_TRAEFIK_NETWORK=).
      args+=(--network "$TRAEFIK_NETWORK")
      args+=(
        -l "traefik.enable=true"
        -l "traefik.tcp.routers.$NAME.entrypoints=${V6POOL_TRAEFIK_ENTRYPOINT:-proxy}"
        -l "traefik.tcp.routers.$NAME.rule=HostSNI(\`*\`)"
        -l "traefik.tcp.services.$NAME.loadbalancer.server.port=${V6POOL_HTTP_PORT:-3128}"
      )
      # Stats stay on the host loopback only when asked; nothing else is
      # ever published, so no port collisions with other containers.
      [ "${V6POOL_PUBLISH_STATS:-false}" = "true" ] \
        && args+=(--publish "127.0.0.1:9090:9090")
    else
      # host networking + NET_ADMIN: binds the host's interfaces directly and
      # can claim source addresses for NDP host-check networks.
      args+=(--network host --cap-add NET_ADMIN)
    fi
    # Forward the V6POOL_* environment for the entrypoint's zero-config
    # bootstrap (V6POOL_USER, V6POOL_PASS, V6POOL_POOL_PREFIX, ...).
    while IFS= read -r var; do
      args+=(-e "$var")
    done < <(env | cut -d= -f1 | grep '^V6POOL_' || true)
    if [ -n "$CONFIG" ]; then
      args+=(-v "$CONFIG:/etc/v6pool/config.yaml:ro")
    else
      [ -n "${V6POOL_POOL_PREFIX:-}" ] || [ -n "${V6POOL_POOL_HOSTS:-}" ] || \
        [ "${V6POOL_AUTO_POOL:-false}" = "true" ] || {
        echo "error: set V6POOL_POOL_PREFIX/V6POOL_POOL_HOSTS/V6POOL_AUTO_POOL or" >&2
        echo "       V6POOL_CONFIG=<path> to a config file" >&2
        exit 1
      }
    fi
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    docker run "${args[@]}" "$IMAGE"
    echo "v6pool started ($(docker inspect -f '{{.State.Status}}' "$NAME"))"
    echo "stats:  http://127.0.0.1:9090/stats?token=..."
    echo "stop:   $0 stop"
    ;;
  stop)
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    echo "v6pool stopped"
    ;;
  restart)
    docker restart "$NAME" >/dev/null 2>&1 || {
      echo "error: no container named $NAME (start it with: $0 run)" >&2
      exit 1
    }
    echo "v6pool restarted"
    ;;
  logs)
    docker logs -f "$NAME"
    ;;
  *)
    usage
    ;;
esac
