#!/usr/bin/env bash
#
# v6pool installer
#
# Detects the machine's IPv6 subnet, configures networking, builds and installs
# the rotating IPv6 proxy (HTTP + SOCKS5), and wires systemd + firewall rules.
#
# Usage:  sudo ./install.sh
#
# To pass overrides, use `sudo -E` (sudo resets the environment by default):
#   PROXY_USER=myuser PROXY_PASS=mypass sudo -E ./install.sh
#
# Overridable environment variables:
#   PROXY_IFACE        network interface to use (auto-detected otherwise)
#   PROXY_PREFIX       IPv6 prefix, e.g. 2001:db8:1:2::  (auto-detected otherwise)
#   PROXY_PREFIX_LEN   prefix length (default 64)
#   PROXY_USER         primary account username (default: generated)
#   PROXY_PASS         primary account password   (default: generated)
#   PROXY_HTTP_PORT    HTTP proxy port   (default 3128)
#   PROXY_SOCKS_PORT   SOCKS5 proxy port (default 1080)
#   PROXY_STATS_PORT   stats port        (default 9090)
#   PROXY_STATS_TOKEN  stats token       (default: generated)
#   PROXY_EXTRA_CONFIG path to an extra YAML fragment appended to config (e.g. extra accounts)

set -euo pipefail

SUDO=""
if [[ $EUID -ne 0 ]]; then
  if command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
  else
    echo "error: run as root or with a sudo-capable user" >&2
    exit 1
  fi
fi

info()  { echo -e "\e[1;34m[i]\e[0m $*"; }
ok()    { echo -e "\e[1;32m[+]\e[0m $*"; }
warn()  { echo -e "\e[1;33m[!]\e[0m $*"; }
die()   { echo -e "\e[1;31m[x]\e[0m $*" >&2; exit 1; }

NOW=$(date +%Y%m%d-%H%M%S)

# ---------------------------------------------------------------------------
# 1. Host checks
# ---------------------------------------------------------------------------
info "Checking IPv6 support..."
if [[ "$(cat /proc/sys/net/ipv6/conf/all/disable_ipv6 2>/dev/null || echo 1)" != "0" ]]; then
  $SUDO sysctl -w net.ipv6.conf.all.disable_ipv6=0 >/dev/null
  $SUDO sh -c 'echo "net.ipv6.conf.all.disable_ipv6 = 0" > /etc/sysctl.d/98-ipv6-enable.conf'
fi

# ---------------------------------------------------------------------------
# 2. Interface detection
# ---------------------------------------------------------------------------
iface="${PROXY_IFACE:-}"
if [[ -z "$iface" ]]; then
  iface=$($SUDO ip -4 route show default 2>/dev/null |
          awk '/^default/{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')
fi
if [[ -z "$iface" ]]; then
  # Robust field scan: `dev` may be preceded by `via <gw>` or not, so a fixed
  # column index (e.g. $7) breaks on some layouts.
  iface=$($SUDO ip -6 route show default 2>/dev/null |
          awk '/^default/{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')
fi
[[ -z "$iface" ]] && die "could not detect the primary network interface (set PROXY_IFACE)"
ok "Using interface: $iface"

mac=$($SUDO cat "/sys/class/net/$iface/address")
[[ -z "$mac" ]] && die "no MAC address for $iface"

# ---------------------------------------------------------------------------
# 3. IPv6 prefix detection
# ---------------------------------------------------------------------------
prefix="${PROXY_PREFIX:-}"
plen="${PROXY_PREFIX_LEN:-64}"
if [[ -z "$prefix" ]]; then
  # preferred: on-link /64 route installed via RA
  prefix=$($SUDO ip -6 route show dev "$iface" 2>/dev/null |
           awk -v p="$plen" '$1 ~ /^[0-9a-f:]+\/'"$plen"'$/ && $2 == "dev" {print $1; exit}' | cut -d/ -f1)
fi
if [[ -z "$prefix" ]]; then
  # fallback: derive from an existing global address (first four hextets)
  ga=$($SUDO ip -6 addr show dev "$iface" scope global 2>/dev/null | awk '/inet6/{print $2; exit}')
  if [[ -n "$ga" ]]; then
    ga=${ga%%/*}
    if [[ "$ga" == *"::"* ]]; then
      # compressed form: expand using python3 if available
      if command -v python3 >/dev/null 2>&1; then
        prefix=$(python3 -c "
import ipaddress
a=ipaddress.IPv6Address('$ga')
g=a.exploded.split(':')
print(':'.join(g[:4])+'::')
")
      fi
    else
      prefix=$(echo "$ga" | awk -F: '{print $1":"$2":"$3":"$4"::"}')
    fi
  fi
fi
[[ -z "$prefix" ]] && die "could not detect IPv6 prefix (set PROXY_PREFIX)"
ok "IPv6 pool prefix: ${prefix}/${plen}"

# ---------------------------------------------------------------------------
# 4. Ensure a global IPv6 address exists
# ---------------------------------------------------------------------------
has_global() {
  $SUDO ip -6 addr show dev "$1" scope global 2>/dev/null | grep -q inet6
}

if ! has_global "$iface"; then
  info "No global IPv6 address on $iface - configuring stateless/DHCPv6 addressing..."
  if [[ -d /etc/netplan ]]; then
    if [[ -f /etc/netplan/50-cloud-init.yaml ]]; then
      $SUDO cp -a /etc/netplan/50-cloud-init.yaml /etc/netplan/50-cloud-init.yaml.bak-"$NOW"
    fi
    cat <<EOF | $SUDO tee /etc/netplan/60-v6pool.yaml >/dev/null
network:
  version: 2
  ethernets:
    $iface:
      match:
        macaddress: "$mac"
      dhcp4: true
      dhcp6: true
      accept-ra: true
      addresses:
        - ${prefix}::2/$plen
EOF
    $SUDO chmod 600 /etc/netplan/60-v6pool.yaml
    $SUDO netplan apply || warn "netplan apply failed - fix manually and re-run"
  elif [[ -d /etc/systemd/network ]]; then
    cat <<EOF | $SUDO tee /etc/systemd/network/10-v6pool.network >/dev/null
[Match]
MACAddress=$mac

[Network]
DHCP=ipv4
LinkLocalAddressing=ipv6
IPv6AcceptRA=yes
DHCPv6=yes

[Address]
Address=${prefix}::2/$plen
EOF
    $SUDO systemctl restart systemd-networkd || true
  else
    warn "no netplan/systemd-networkd - configure IPv6 on $iface manually, then re-run"
  fi
  info "Waiting up to 90s for a global IPv6 address..."
  for _ in $(seq 1 18); do
    has_global "$iface" && break
    sleep 5
  done
  has_global "$iface" || die "no global IPv6 address arrived on $iface"
else
  info "Global IPv6 address already present on $iface"
fi

# ---------------------------------------------------------------------------
# 5. Test whether the whole prefix is routable (pool mode vs single-address)
# ---------------------------------------------------------------------------
nonlocal="$($SUDO cat /proc/sys/net/ipv6/ip_nonlocal_bind 2>/dev/null || echo 0)"
[[ "$nonlocal" != "1" ]] && $SUDO sysctl -w net.ipv6.ip_nonlocal_bind=1 >/dev/null

test_addr="${prefix%::}::3"
pool_mode=0
if command -v curl >/dev/null 2>&1; then
  $SUDO ip -6 route add local "${prefix}/${plen}" dev "$iface" 2>/dev/null || true
  # Claim the probe address first: some tethers (NDP host-check) only forward
  # traffic from addresses that have been announced on the wire, so testing
  # from an unclaimed address would wrongly report "single address" mode.
  $SUDO ip -6 addr add "${test_addr}/128" dev "$iface" nodad 2>/dev/null || true
  out=$(timeout 8 curl -s --max-time 7 -6 --interface "$test_addr" https://api6.ipify.org 2>/dev/null || true)
  $SUDO ip -6 addr del "${test_addr}/128" dev "$iface" 2>/dev/null || true
  if [[ -n "$out" && "$out" != *"Couldn't bind"* ]]; then
    pool_mode=1
  else
    $SUDO ip -6 route del local "${prefix}/${plen}" dev "$iface" 2>/dev/null || true
  fi
fi

if [[ $pool_mode -eq 1 ]]; then
  ok "Whole-prefix routing works - FULL POOL mode (${prefix}/${plen})"
  $SUDO sh -c 'echo "net.ipv6.ip_nonlocal_bind = 1" > /etc/sysctl.d/99-v6pool.conf'
  $SUDO sysctl --system >/dev/null 2>&1 || true
else
  ok "Whole-prefix routing not available - ADDRESS LIST / SINGLE mode"
  warn "This provider only routes the IPv6 addresses that are configured on the"
  warn "host (e.g. static /128s on Hetzner/OVH/Vultr, allocated IPv6 objects on"
  warn "Oracle, DHCPv6-assigned addresses). To still get rotation:"
  warn "  1. Get several addresses from your provider and pass them to this"
  warn "     script:  PROXY_POOL_HOSTS='addr1 addr2 ...' ./install.sh"
  warn "  2. Or, on an already-installed box:"
  warn "     sudo ./scripts/add-pool-hosts.sh addr1 addr2 ..."
  if [[ -n "${PROXY_POOL_HOSTS:-}" ]]; then
    ok "Using PROXY_POOL_HOSTS for rotation"
  fi
fi

# The local route + ip_nonlocal_bind are always wanted: they let the kernel
# accept return traffic for pool addresses (needed for address-list pools too).
$SUDO sh -c 'echo "net.ipv6.ip_nonlocal_bind = 1" > /etc/sysctl.d/99-v6pool.conf'
$SUDO sysctl --system >/dev/null 2>&1 || true
$SUDO ip -6 route add local "${prefix}/${plen}" dev "$iface" 2>/dev/null || true

# ---------------------------------------------------------------------------
# 6. Tooling: Go + curl (installed only if missing)
# ---------------------------------------------------------------------------
pm=""
if command -v apt-get >/dev/null 2>&1; then pm=apt-get
elif command -v dnf >/dev/null 2>&1; then pm=dnf
elif command -v pacman >/dev/null 2>&1; then pm=pacman
elif command -v zypper >/dev/null 2>&1; then pm=zypper
elif command -v apk >/dev/null 2>&1; then pm=apk
fi

pm_install() {
  local pkg="$1"
  [[ -n "$pm" ]] || return 1
  info "Installing $pkg via $pm..."
  case "$pm" in
    apt-get) $SUDO apt-get update >/dev/null 2>&1 || true
             $SUDO apt-get install -y "$pkg" >/dev/null 2>&1 ;;
    dnf)     $SUDO dnf install -y "$pkg" >/dev/null 2>&1 ;;
    pacman)  $SUDO pacman -S --noconfirm "$pkg" >/dev/null 2>&1 ;;
    zypper)  $SUDO zypper -n install "$pkg" >/dev/null 2>&1 ;;
    apk)     $SUDO apk add --no-cache "$pkg" >/dev/null 2>&1 ;;
  esac
}

pm_pkg() {
  case "$pm/$1" in
    apt-get/go) echo golang-go ;;
    *) echo "$1" ;;
  esac
}

if ! command -v go >/dev/null 2>&1; then
  info "Installing Go..."
  pm_install "$(pm_pkg go)" || true
  if ! command -v go >/dev/null 2>&1; then
    arch=$(uname -m)
    case "$arch" in
      x86_64)  goarch=amd64 ;;
      aarch64) goarch=arm64 ;;
      *) die "unsupported arch $arch - install Go manually" ;;
    esac
    if command -v curl >/dev/null 2>&1; then
      $SUDO curl -fsSL -o /tmp/go.tgz "https://go.dev/dl/go1.24.0.linux-$goarch.tar.gz"
    elif command -v wget >/dev/null 2>&1; then
      $SUDO wget -qO /tmp/go.tgz "https://go.dev/dl/go1.24.0.linux-$goarch.tar.gz"
    else
      die "need curl or wget to download Go - install one manually"
    fi
    $SUDO rm -rf /usr/local/go && $SUDO tar -C /usr/local -xzf /tmp/go.tgz
    export PATH=$PATH:/usr/local/go/bin
  fi
  command -v go >/dev/null 2>&1 || die "Go still unavailable - install it manually"
fi

if ! command -v curl >/dev/null 2>&1; then
  pm_install curl || warn "curl not available - pool auto-test will be skipped"
fi

# ---------------------------------------------------------------------------
# 7. Build the proxy
# ---------------------------------------------------------------------------
src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ ! -f "$src/go.mod" ]]; then
  die "install.sh must be run from the v6pool source directory"
fi
cd "$src"
info "Building v6pool..."
GOPATH="${GOPATH:-$HOME/go}" go build -trimpath -o v6pool ./cmd/v6pool
ok "Built $src/v6pool"

# ---------------------------------------------------------------------------
# 8. Install binary + config (preserving existing accounts)
# ---------------------------------------------------------------------------
$SUDO mkdir -p /etc/v6pool
$SUDO cp -f "$src/v6pool" /usr/local/bin/v6pool

conf=/etc/v6pool/config.yaml
if [[ ! -f $conf ]]; then
  user="${PROXY_USER:-user-$(head -c4 /dev/urandom | od -An -tx1 | tr -d ' \n')}"
  pass="${PROXY_PASS:-$(head -c12 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c16)}"
  stats_token="${PROXY_STATS_TOKEN:-$(head -c12 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c16)}"
  cat <<EOF | $SUDO tee $conf >/dev/null
http_listen: ":${PROXY_HTTP_PORT:-3128}"
socks5_listen: ":${PROXY_SOCKS_PORT:-1080}"
stats_listen: "127.0.0.1:${PROXY_STATS_PORT:-9090}"
stats_token: "$stats_token"

$(if [[ -n "${PROXY_POOL_HOSTS:-}" ]]; then
  echo "pool_hosts:"
  for h in $PROXY_POOL_HOSTS; do echo "  - \"$h\""; done
else
  echo "pool_prefix: \"$prefix\""
  echo "pool_bits: $plen"
  if [[ $pool_mode -eq 0 ]]; then echo "source_iface: \"$iface\""; fi
fi)

sticky_ttl_seconds: 300
avoid_recent: 128
dial_timeout_seconds: 15

accounts:
  - name: primary
    username: $user
    password: $pass
EOF
  ok "Created $conf (primary account: $user / $pass)"
else
  warn "$conf exists - updating network settings, keeping existing accounts"
  $SUDO cp -f $conf "$conf.bak-$NOW"
  if [[ -n "${PROXY_USER:-}" || -n "${PROXY_PASS:-}" ]]; then
    user="${PROXY_USER:-$(grep -A2 'name: primary' $conf | grep username | awk '{print $2}' | tr -d '"')}"
    pass="${PROXY_PASS:-$(grep -A2 'name: primary' $conf | grep password | awk '{print $2}' | tr -d '"')}"
    $SUDO awk -v u="$user" -v p="$pass" '
      /name: primary/ {inp=1}
      inp && /username:/ { sub(/username:.*/, "username: " u); inp=2 }
      inp == 2 && /password:/ { sub(/password:.*/, "password: " p); inp=3 }
      { print }' $conf > $conf.tmp && $SUDO mv $conf.tmp $conf
    ok "Updated primary account: $user / $pass"
  fi
  set_key() {
    local key="$1" val="$2"
    if $SUDO grep -q "^${key}:" $conf; then
      $SUDO sed -i "s|^${key}:.*|${key}: ${val}|" $conf
    else
      $SUDO sed -i "1i ${key}: ${val}" $conf
    fi
  }
  set_key http_listen "\":${PROXY_HTTP_PORT:-3128}\""
  set_key socks5_listen "\":${PROXY_SOCKS_PORT:-1080}\""
  set_key stats_listen "\"127.0.0.1:${PROXY_STATS_PORT:-9090}\""
  if [[ -n "${PROXY_POOL_HOSTS:-}" ]]; then
    $SUDO sed -i '/^pool_hosts:/,/^[a-z]/d' $conf
    $SUDO sed -i '/^pool_prefix:/d' $conf
    $SUDO sed -i '/^pool_bits:/d' $conf
    $SUDO sed -i '/^source_iface:/d' $conf
    { echo "pool_hosts:"; for h in $PROXY_POOL_HOSTS; do echo "  - \"$h\""; done; } | $SUDO tee -a $conf >/dev/null
  else
    set_key pool_prefix "\"$prefix\""
    set_key pool_bits "$plen"
    if [[ $pool_mode -eq 1 ]]; then
      $SUDO sed -i '/^fixed_source:/d' $conf
      $SUDO sed -i '/^source_iface:/d' $conf
      $SUDO sed -i '/^pool_hosts:/,/^[a-z]/d' $conf
    else
      set_key source_iface "$iface"
      $SUDO sed -i '/^fixed_source:/d' $conf
      $SUDO sed -i '/^pool_hosts:/,/^[a-z]/d' $conf
    fi
  fi
fi

if [[ -n "${PROXY_EXTRA_CONFIG:-}" ]]; then
  $SUDO cat "$PROXY_EXTRA_CONFIG" >> $conf
  info "Appended extra config from $PROXY_EXTRA_CONFIG"
fi

# ---------------------------------------------------------------------------
# 9. systemd units
# ---------------------------------------------------------------------------
if [ -f "$(dirname "$0")/v6pool.service" ]; then
  # Single source of truth: the unit shipped in the repo (grants CAP_NET_ADMIN
  # so claim mode can add source addresses).
  $SUDO install -m 644 "$(dirname "$0")/v6pool.service" /etc/systemd/system/v6pool.service
else
  cat <<EOF | $SUDO tee /etc/systemd/system/v6pool.service >/dev/null
[Unit]
Description=IPv6 rotating proxy (HTTP + SOCKS5)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/v6pool -config /etc/v6pool/config.yaml
Restart=on-failure
RestartSec=3
DynamicUser=yes
NoNewPrivileges=false
AmbientCapabilities=CAP_NET_ADMIN
CapabilityBoundingSet=CAP_NET_ADMIN

[Install]
WantedBy=multi-user.target
EOF
fi

# Local route for the pool range is always wanted (address-list pools need the
# return path accepted by the kernel too).
cat <<EOF | $SUDO tee /etc/systemd/system/ipv6-localroute.service >/dev/null
[Unit]
Description=Local route for IPv6 pool
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'ip -6 route add local $prefix/$plen dev $iface || true'
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF
$SUDO systemctl enable ipv6-localroute >/dev/null 2>&1 || true
$SUDO systemctl start ipv6-localroute >/dev/null 2>&1 || true

if [[ $pool_mode -eq 1 ]]; then
  if ! command -v ndppd >/dev/null 2>&1; then
    pm_install ndppd || true
  fi
  if command -v ndppd >/dev/null 2>&1; then
    cat <<EOF | $SUDO tee /etc/ndppd.conf >/dev/null
route-ttl 30000

proxy $iface {
  router no
  timeout 500
  ttl 30000
  rule $prefix/$plen {
    static
  }
}
EOF
    $SUDO systemctl enable ndppd >/dev/null 2>&1 || true
    $SUDO systemctl restart ndppd >/dev/null 2>&1 || true
  else
    warn "ndppd not installed - incoming traffic to pool addresses may not be delivered"
  fi
else
  if command -v ndppd >/dev/null 2>&1; then
    $SUDO systemctl disable ndppd >/dev/null 2>&1 || true
    $SUDO systemctl stop ndppd >/dev/null 2>&1 || true
  fi
fi

# ---------------------------------------------------------------------------
# 10. Firewall
# ---------------------------------------------------------------------------
if command -v ufw >/dev/null 2>&1 && $SUDO ufw status >/dev/null 2>&1; then
  $SUDO ufw allow "${PROXY_HTTP_PORT:-3128}/tcp" >/dev/null 2>&1 || true
  $SUDO ufw allow "${PROXY_SOCKS_PORT:-1080}/tcp" >/dev/null 2>&1 || true
  ok "ufw: allowed HTTP ${PROXY_HTTP_PORT:-3128} and SOCKS5 ${PROXY_SOCKS_PORT:-1080}"
fi

# ---------------------------------------------------------------------------
# 11. Start
# ---------------------------------------------------------------------------
$SUDO systemctl daemon-reload
$SUDO systemctl enable v6pool >/dev/null 2>&1 || true
$SUDO systemctl restart v6pool

sleep 1
if $SUDO systemctl is-active v6pool >/dev/null 2>&1; then
  ok "v6pool is RUNNING"
else
  die "v6pool failed to start - check: journalctl -u v6pool"
fi

ip4=$($SUDO ip -4 addr show dev "$iface" | awk '/inet /{print $2; exit}' | cut -d/ -f1)

echo
echo "================================================================"
echo "  v6pool installed"
echo "----------------------------------------------------------------"
echo "  HTTP proxy : http://<user>:<pass>@${ip4:-<this-host>}:${PROXY_HTTP_PORT:-3128}"
echo "  SOCKS5     : <user>:<pass>@${ip4:-<this-host>}:${PROXY_SOCKS_PORT:-1080}"
echo "  Stats      : http://127.0.0.1:${PROXY_STATS_PORT:-9090}/stats (token-gated, loopback only)"
echo "  Pool       : $([ -n "${PROXY_POOL_HOSTS:-}" ] && echo "ADDRESS LIST (${PROXY_POOL_HOSTS})" || echo "$prefix/$plen  (mode: $([ $pool_mode -eq 1 ] && echo FULL POOL || echo SINGLE ADDRESS))")"
echo "  Sticky     : username like  <user>-session-<token>  pins one IP per session"
echo "================================================================"
