# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Exact breadth-first rotation for routed-prefix pools of any length
  (`pool_bits: 1`-`128`): each per-destination cycle walks a permuted
  bit-reversed counter, so after 2 picks both `/n+1` halves are used, after 4
  all `/n+2` quarters, and so on bit by bit — distinct exits spread across
  aggregates at every granularity rate limiters key on, with zero tracking
  (O(1) state: position, odd stride, mask). Odd strides scramble visit order
  without disturbing coverage; per-destination tweaks give each site an
  independent cycle; a small global recent-IP ring guards cross-stream
  coincidences. Exits use full-entropy placement (reserved IDs and EUI-64
  `ff:fe` markers skipped via cycle advance), a random per-boot stream seed
  (`pool_seed`, hex for reproducible streams) so restarts never replay, and
  per-account stream domains so accounts never share sequences. New `/stats`
  counters: picks, cycle skips, destinations tracked, seed fingerprint.
  `/128` pins its single address; out-of-range `pool_bits` and bad
  `pool_seed` are rejected at config load. Sliced accounts keep keyed
  uniform mapping over their range.
- Zero-config Docker: `docker/entrypoint.sh` generates a config from `V6POOL_*`
  environment variables when none is mounted; `docker-run.sh` run/stop/restart/
  logs helper; `docker-compose.yml` for compose users.
- Version injection: Docker builds and goreleaser artifacts stamp
  `main.version` (log line reports it at startup).
- Release pipeline: goreleaser (linux/darwin/windows, amd64/arm64/arm/386,
  checksums) on `v*` tags; ghcr image tags include branch/tag/sha/`latest`.
- Go module restructure: `cmd/v6pool` + `internal/` packages (config,
  ifaceutil, pool, claim, metrics, proxy) with unit tests (SOCKS5 handshake +
  relay over `net.Pipe`, rotation, sticky sessions, auto-pool, claiming).
- CI: lint (golangci-lint + shellcheck) and build jobs on Go 1.24.
- Makefile: build (with `git describe` version), test, vet, fmt, lint,
  release (goreleaser snapshot), install targets.
- Installer fixes: interface detection on systems without `dev` in `ip -6
  route show` output, Go 1.24.0 download, `-config` flag wiring, shellcheck
  clean.
- Docker zero-config: `V6POOL_STATS_PORT` (metrics port on 127.0.0.1,
  `V6POOL_STATS_LISTEN` still overrides the full bind address) plus
  `V6POOL_DISABLE_HTTP` / `V6POOL_DISABLE_SOCKS5` / `V6POOL_DISABLE_STATS`
  to leave individual listeners off.
- Config: `enable_http` / `enable_socks5` / `enable_stats` booleans (all
  default true; absent ≠ disabled, only an explicit `false` turns a
  listener off). `stats_listen: ""` continues to disable the stats server.

### Changed

- Default HTTP proxy port 8080 → **3128**.
- Docker image runs through `entrypoint.sh` (signal-clean `exec`) instead of
  the binary directly; Alpine runtime now includes `iproute2` so address
  claiming works in containers.
- `v6pool.service` passes `-config /etc/v6pool/config.yaml`.

### Fixed

- Deadlock in sticky-session IP selection (mutex re-lock while held).
- Dockerfile `ARG VERSION` scope (not visible to build stage → empty version).
- `docker-run.sh` not forwarding `V6POOL_*` env to the container.
