// Package ifaceutil inspects network interfaces and routes.
package ifaceutil

import (
	"net"
	"os"
	"strings"
)

// GlobalIP returns the global unicast IPv6 address of the named interface
// with the longest prefix length, or nil when the interface has none.
func GlobalIP(name string) net.IP {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	var best net.IP
	bestLen := -1
	for _, a := range addrs {
		ip, ipnet, err := net.ParseCIDR(a.String())
		if err != nil {
			continue
		}
		if ip.To4() != nil || ip.IsLinkLocalUnicast() || ip.IsLoopback() {
			continue
		}
		ones, _ := ipnet.Mask.Size()
		if ones > bestLen {
			bestLen = ones
			best = ip.To16()
		}
	}
	return best
}

// Addrs returns the IP networks assigned to the named interface, or nil.
func Addrs(name string) []*net.IPNet {
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

// HasAddrInPrefix reports whether the interface has a global address whose
// network contains src — used to auto-detect the link that owns a prefix.
func HasAddrInPrefix(iface net.Interface, src net.IP) bool {
	for _, ipnet := range Addrs(iface.Name) {
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

// PrefixFromAddr masks an IPv6 address to the given prefix length (default
// 64 when bits <= 0, capped at 128).
func PrefixFromAddr(ip net.IP, bits int) net.IP {
	if bits <= 0 {
		bits = 64
	}
	if bits > 128 {
		bits = 128
	}
	return ip.Mask(net.CIDRMask(bits, 128))
}

// DefaultRouteIface returns the interface name of the default IPv6 route, or
// "" when the host has none. It parses /proc/net/ipv6_route, so it is Linux
// only; the kernel reports the interface that owns the default route.
func DefaultRouteIface() string {
	data, err := os.ReadFile("/proc/net/ipv6_route")
	if err != nil {
		return ""
	}
	return ParseRouteTable(string(data))
}

// ParseRouteTable extracts the default-route interface from a
// /proc/net/ipv6_route dump. A default route is a row with a zero destination
// and a zero prefix length; the interface name is the last field. Loopback
// default routes (blackhole entries) are ignored.
func ParseRouteTable(data string) string {
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		if fields[0] == "00000000000000000000000000000000" && fields[1] == "00" {
			if iface := fields[len(fields)-1]; iface != "lo" {
				return iface
			}
		}
	}
	return ""
}
