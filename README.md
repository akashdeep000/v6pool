# v6pool

Rotating IPv6 proxy — every request exits from a different IPv6 address of your
subnet. HTTP + SOCKS5, sticky sessions, per-account sub-pools, stats + Prometheus
metrics, address claiming for tether/routed prefixes.

## Features

- **Rotating** — every request walks a permuted breadth-first cycle over the
  pool: after 2 picks both `/49` halves are used, after 4 all `/50` quarters,
  and so on bit by bit through every prefix level — a fresh `/56`, then a
  fresh `/64` inside reused `/56s`, then fresh IPs inside reused `/64s`.
  Exact by construction (no tracking, no search, no fallback): distinct IPs
  always mean distinct `/64s` on pools shorter than `/64` (Bright-Data
  style). Any prefix length works (`pool_bits: 1`-`128`); O(1) state per
  destination (a counter, a stride, a mask).
- **Per-destination cycles** — each upstream host gets an independent cycle
  (keyed tweak + own counter), so hammering one site never reuses an
  aggregate recently shown to *that* site; a small global recent-IP ring
  guards against cross-stream coincidences.
- **Unlinkable exits** — full-entropy placement (reserved IDs and EUI-64
  `ff:fe` markers skipped via cycle advance), per-boot random stream seed
  (`pool_seed`) so restarts never replay sequences, and per-account stream
  domains so accounts never walk the same addresses.
- **Sticky sessions** — username `user-session-<token>` pins one IP for that
  token until `sticky_ttl_seconds` (default 300s) passes
- **HTTP + SOCKS5 + stats** on three ports, basic auth, IPv6-only
- **Per-account sub-pools** — slice the pool into ranges per account (IID
  range for `/64` pools, `/64` range for shorter pools like `/48`)
- **Auto-pool for dynamic /64s** — `auto_pool` re-derives the pool from the
  interface's live address on every pick, so rotation survives DHCPv6/SLAAC
  prefix changes with no restart, cron or helper services
- **Address claiming** — auto-claims each picked source (`ip addr add … nodad`)
  when the prefix is routed but addresses aren't pre-assigned (phone
  USB/Wi-Fi tether, pure-routed providers); a no-op when addresses are already
  on the interface
- **Request logging** — one line per request (user, session, method, host,
  status, source IP, latency) when `log_requests: true`
- **Metrics** — JSON `/stats` and Prometheus `/metrics` (dial-error breakdown,
  per-account counters, rotation picks, cycle skips, destinations tracked,
  stream seed) on the stats port, token-gated

## Quick start

### Docker (zero config)

```bash
V6POOL_USER=user V6POOL_PASS=pass \
V6POOL_STATS_TOKEN=token \
./docker-run.sh
```

or with compose — fill in the env block and `docker compose up -d` (see
`docker-compose.yml`). **`auto_pool` is the default**: the /64 is derived from
the default-route interface's live address, so rotation just works on a VPS or
a phone-tether /64 and tracks prefix changes on its own. A minimal config is
generated from the `V6POOL_*` environment; alternatively mount your own config
(`V6POOL_CONFIG=/path/to/config.yaml ./docker-run.sh` or a compose volume).

`--network host` + `--cap-add NET_ADMIN` are already set up: the proxy binds
source addresses on the host's network stack and can claim them.

### systemd (installer)

```bash
git clone https://github.com/akashdeep000/v6pool.git && cd v6pool
sudo ./install.sh
```

The installer detects the primary interface + prefix, tests whether the
**whole prefix is routable** and picks the mode automatically:

| Mode | Providers | Rotation |
|---|---|---|
| Full pool (`pool_prefix` + `pool_bits`) | Hetzner, OVH, Vultr, Scaleway… | entire /64 |
| Full prefix pool (`pool_prefix` + `pool_bits: 48`) | tunnel brokers with a routed prefix, any routed /48+ | entire prefix, fresh aggregates per pick |
| Address list (`pool_hosts`) | any provider that routes only configured addresses | list of /128s on the host |
| Single address (`source_iface` / `fixed_source`) | any | one address (fallback) |
| Live pool (`source_iface` + `auto_pool`) | dynamic /64s: phone USB/Wi-Fi tether, DHCPv6/SLAAC roam | live /64, tracks prefix changes automatically |

Env overrides: `PROXY_IFACE`, `PROXY_PREFIX`, `PROXY_PREFIX_LEN`, `PROXY_USER`,
`PROXY_PASS`, `PROXY_HTTP_PORT` (3128), `PROXY_SOCKS_PORT` (1080),
`PROXY_STATS_PORT` (9090), `PROXY_STATS_TOKEN`, `PROXY_POOL_HOSTS`
(space-separated address list), `PROXY_EXTRA_CONFIG` (path to append to
config). Pass them with `sudo -E` — sudo resets the environment by default.
Re-running the installer updates network settings while keeping your existing
accounts.

## Usage

| Protocol | Endpoint |
|---|---|
| HTTP | `http://USER:PASS@HOST:3128` |
| SOCKS5 | `USER:PASS@HOST:1080` |
| Stats | `http://127.0.0.1:9090/stats` and `/metrics` (token via `?token=` or `Authorization: Bearer`; loopback-only by default) |

```bash
# each request exits from a different IPv6
curl -x http://USER:PASS@HOST:3128 https://api6.ipify.org

# sticky session: same IP for the whole session token
curl -x http://USER-session-abc123:PASS@HOST:3128 https://api6.ipify.org

# stats
curl "http://127.0.0.1:9090/stats?token=TOKEN"        # JSON
curl "http://127.0.0.1:9090/metrics?token=TOKEN"      # Prometheus text
```

## Configuration (`/etc/v6pool/config.yaml`)

See `config.example.yaml` for the full annotated example. Key settings:

```yaml
pool_prefix: "2001:db8:1:2::"   # Option A: routed range
pool_bits: 64                   # any 1-128 (0 = default 64); below /64 the
                                # breadth-first cycle lands every pick in a
                                # fresh /64 until all are used, then fresh
                                # IPs inside reused /64s, and so on per bit
# pool_seed: ""                 # empty = random stream per boot (restarts
                                # never replay); 1-16 hex chars = fixed,
                                # reproducible stream (tests)
# pool_hosts:                            # Option B: explicit addresses
#   - "2001:db8:1:2:0:aaaa:bbbb:cccc:1"
# source_iface: "enp0s6"                 # Option C: single address (auto)
# auto_pool: true                        # Option D: track a dynamic /64 live
sticky_ttl_seconds: 300
avoid_recent: 128             # global recent-IP ring depth: guards against
                              # cross-stream coincidences (0 = off)
# avoid_hosts_max: 1024       # max per-destination cycles tracked (LRU)
log_requests: true
# stats_token: "CHANGE_ME"               # gates /stats and /metrics
# claim_iface: ""            # optional; auto-detected from the pool prefix
# claim_ttl_seconds: 300     # idle claimed addresses are removed after this

accounts:
  - name: primary
    username: user
    password: pass
  - name: scrapers                      # optional slice of the pool
    username: user2
    password: pass2
    start: 0                            # address index 0..size-1 (/64 pools);
                                        # /64 index 0..size-1 for pools < /64
    size: 1000000
```

With a `/48` pool the proxy needs the usual tunnel plumbing: the `/48`
routed at the broker to your host, a `local <prefix>/48` route plus
`net.ipv6.ip_nonlocal_bind=1` so any pool address can be bound, and a
default route (or policy rule) sending pool-sourced traffic back through
the tunnel. The installer sets the local route + sysctl for you; the
tunnel device itself (SIT/WireGuard/…) is outside v6pool's scope.

## Address claiming (tether / routed prefixes)

By default v6pool binds source addresses that are already assigned to an
interface — the normal VPS case, with zero extra setup. Sometimes a working
address is **routed to the host but not assigned** to any interface; the
provider/device then only forwards traffic from an address it has seen claimed
on the wire (a common NDP host-check, e.g. behind a phone's USB/Wi-Fi
tethering). In that case v6pool automatically claims each picked source with

```sh
ip -6 addr add <ip>/128 dev <iface> nodad
```

before dialing (and removes idle ones after `claim_ttl_seconds`). It detects
the interface from the pool prefix and skips the work entirely when the address
is already bound, so this is fully automatic and safe on any setup. Claiming
needs `CAP_NET_ADMIN` (or root); the shipped `v6pool.service` unit already
grants it via `AmbientCapabilities=CAP_NET_ADMIN` (and the Docker setup via
`--cap-add=NET_ADMIN`).

### Dynamic /64 (phone tether reconnects)

When the prefix is dynamic (e.g. a phone's tether /64 that changes on each
reconnect), the simplest setup is **`auto_pool`** — v6pool re-reads the
interface's current global address on every pick and derives the pool from it,
so rotation survives prefix changes with zero intervention:

```yaml
source_iface: enp0s20f0u2   # tether link
auto_pool: true
```

No restart, no cron/timer, no extra services. Works with claiming exactly like
`pool_prefix` mode — the interface is auto-detected from the pool prefix.

For static configs, `sudo ./scripts/update-prefix.sh` still exists to refresh
a hard-coded `pool_prefix` after tether reconnects (or run it manually).

## Address-list mode (`pool_hosts`)

Some providers only route the individual IPv6 addresses configured on the
host — the whole-prefix pool test fails on them. Fix: put several addresses on
the host and list them in the config. Any origin works: static /128s added to
the interface (Hetzner, OVH, …), IPv6 objects allocated to the VNIC (Oracle,
up to 32 per VNIC), DHCPv6/SLAAC leases, …

```bash
sudo ./scripts/add-pool-hosts.sh 2001:db8:1:2:0:aaaa:bbbb:1 2001:db8:1:2:0:aaaa:bbbb:2 2001:db8:1:2:0:aaaa:bbbb:3   # existing install
PROXY_POOL_HOSTS="2001:db8:1:2:0:aaaa:bbbb:1 2001:db8:1:2:0:aaaa:bbbb:2 2001:db8:1:2:0:aaaa:bbbb:3" sudo ./install.sh   # fresh install
```

The script rewrites the config (`pool_hosts` block, drops `pool_prefix` /
`source_iface`) and restarts the service. The installer sets up the required
network plumbing (`ip_nonlocal_bind=1`, local route for the prefix) in every
mode, so no extra steps are needed.

## Docker

Images are published to the GitHub Container Registry (`:latest` tracks
`main`, tags like `v0.1.0` track releases):

```bash
docker pull ghcr.io/akashdeep000/v6pool:latest
V6POOL_USER=user V6POOL_PASS=pass \
V6POOL_POOL_PREFIX=2001:db8:1:2:: \
V6POOL_STATS_TOKEN=token \
./docker-run.sh          # or docker compose up -d
```

All `V6POOL_*` variables mirror the config keys — see `docker-compose.yml` for
the full list. To build locally instead: `docker build -t v6pool .`

## Releases

Binaries for linux/darwin/windows (amd64/arm64/arm/386) are published on every
`v*` tag by goreleaser, as `.tar.gz`/`.zip` archives with checksums. Docker
images are built in the same workflow.

## Development

```bash
make build     # static binary with injected version
make test      # go test ./...
make vet       # go vet ./...
make lint      # golangci-lint run
make release   # goreleaser --snapshot --clean (local release dry-run)
```

The codebase is Go 1.24, module `github.com/akashdeep000/v6pool`, with
`cmd/v6pool` (CLI) and `internal/` packages (`config`, `ifaceutil`, `pool`,
`claim`, `metrics`, `proxy`).

## Testing

```bash
curl -s -x http://USER:PASS@HOST:3128 https://api6.ipify.org   # changes per call
journalctl -u v6pool --no-pager                             # request log lines
```

## License

Apache-2.0 — see [LICENSE](LICENSE).
