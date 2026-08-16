// Package claim adds and removes IPv6 /128 addresses on interfaces via the
// system `ip` command. This is only needed for tether / routed-prefix setups
// where the upstream performs an NDP host-check and only forwards traffic
// from addresses it has seen claimed on the wire. Requires CAP_NET_ADMIN.
package claim

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultTTL is the idle time after which a claimed address is removed when
// no explicit TTL is configured.
const DefaultTTL = 300 * time.Second

// Claimer claims /128 nodad addresses on an interface and sweeps idle ones.
type Claimer struct {
	iface  string // configured interface, may be empty for auto-detect
	ttl    time.Duration
	mu     sync.Mutex
	claims map[string]time.Time // last-seen time per claimed address
	cached string               // auto-detected interface name
}

// New returns a Claimer for the given interface (empty = auto-detect from the
// pool prefix) and idle TTL in seconds (<= 0 uses DefaultTTL).
func New(iface string, ttlSeconds int) *Claimer {
	ttl := time.Duration(ttlSeconds) * time.Second
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Claimer{
		iface:  iface,
		ttl:    ttl,
		claims: make(map[string]time.Time),
	}
}

// EnsureClaimed binds src to the tether interface so upstream NDP host-checks
// pass. If src is already assigned to the interface (the common VPS case),
// this is a no-op: nothing is tracked for later removal. It returns an error
// only when the address could not be claimed.
func (c *Claimer) EnsureClaimed(src net.IP) error {
	ifname, onIface := c.claimIface(src)
	if ifname == "" || onIface {
		return nil
	}
	c.mu.Lock()
	c.claims[src.String()] = time.Now()
	c.mu.Unlock()
	return claimAddr(ifname, src)
}

// Sweep removes claimed addresses that have been idle for the TTL. It is
// expected to be called periodically.
func (c *Claimer) Sweep() {
	c.mu.Lock()
	var stale []string
	staleName := c.iface
	if staleName == "" {
		staleName = c.cached
	}
	for ipStr, last := range c.claims {
		if time.Since(last) > c.ttl {
			stale = append(stale, ipStr)
		}
	}
	c.mu.Unlock()
	if len(stale) == 0 {
		return
	}
	for _, ipStr := range stale {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if err := delAddr(staleName, ip); err == nil {
			c.mu.Lock()
			if last, ok := c.claims[ipStr]; ok && time.Since(last) > c.ttl {
				delete(c.claims, ipStr)
			}
			c.mu.Unlock()
		}
	}
}

// claimIface returns the interface that should carry claimed addresses for
// src — the configured one, or the interface that already has a global
// address within the same /64 as src (the tether link), caching the name
// after the first successful detection. The second return value reports
// whether src is already assigned to that interface.
func (c *Claimer) claimIface(src net.IP) (string, bool) {
	name := c.iface
	c.mu.Lock()
	if name == "" {
		name = c.cached
	}
	c.mu.Unlock()
	if name == "" {
		ifaces, err := net.Interfaces()
		if err != nil {
			return "", false
		}
		for _, iface := range ifaces {
			if ifaceHasAddrInPrefix(iface, src) {
				name = iface.Name
				break
			}
		}
		if name == "" {
			return "", false
		}
		c.mu.Lock()
		c.cached = name
		c.mu.Unlock()
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return "", false
	}
	onIface := false
	for _, ip := range ifaceAddrs(iface.Name) {
		if ip != nil && ip.IP.Equal(src) {
			onIface = true
			break
		}
	}
	return iface.Name, onIface
}

// claimAddr adds ip as a /128 nodad address on ifname. nodad skips duplicate
// address detection so the address is usable immediately; the kernel then
// answers NDP solicitations for it. Returns nil when the address is (or
// becomes) assigned.
func claimAddr(ifname string, ip net.IP) error {
	out, err := exec.Command("ip", "-6", "addr", "add",
		ip.String()+"/128", "dev", ifname, "nodad").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "File exists") || strings.Contains(msg, "already assigned") {
			return nil
		}
		return fmt.Errorf("ip: %s", msg)
	}
	return nil
}

// delAddr removes a claimed address; missing addresses are treated as success.
func delAddr(ifname string, ip net.IP) error {
	out, err := exec.Command("ip", "-6", "addr", "del",
		ip.String()+"/128", "dev", ifname).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "Cannot assign requested address") || strings.Contains(msg, "File exists") {
			return nil
		}
		return fmt.Errorf("ip: %s", msg)
	}
	return nil
}

func ifaceHasAddrInPrefix(iface net.Interface, src net.IP) bool {
	for _, ipnet := range ifaceAddrs(iface.Name) {
		if ipnet == nil || !ipnet.Contains(src) {
			continue
		}
		ip := ipnet.IP
		if ip.To4() != nil || ip.IsLinkLocalUnicast() || ip.IsLoopback() {
			continue
		}
		return true
	}
	return false
}

func ifaceAddrs(name string) []*net.IPNet {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	out := make([]*net.IPNet, 0, len(addrs))
	for _, a := range addrs {
		_, ipnet, err := net.ParseCIDR(a.String())
		if err == nil {
			out = append(out, ipnet)
		}
	}
	return out
}
