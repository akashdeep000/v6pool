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
# Subcommands: run (default), stop, logs, restart
set -euo pipefail

IMAGE="${V6POOL_IMAGE:-ghcr.io/akashdeep000/v6pool:latest}"
NAME="${V6POOL_NAME:-v6pool}"
CONFIG="${V6POOL_CONFIG:-}"

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
      --network host
      --cap-add NET_ADMIN
    )
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
