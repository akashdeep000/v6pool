# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `freebind: true` config option: rotates from addresses not assigned to an
  interface via the unprivileged `IP_FREEBIND` socket option — enables
  rotation in Termux/Android without root on networks that route a delegated
  prefix (DHCPv6 PD) to the device. Also wired to `V6POOL_FREEBIND` in the
  docker entrypoint.

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
