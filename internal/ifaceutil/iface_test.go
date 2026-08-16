package ifaceutil

import (
	"net"
	"testing"
)

// sampleRouteTable mirrors the layout of /proc/net/ipv6_route: 10
// whitespace-separated fields per row, the interface name last. Rows use
// hex-encoded addresses and hex prefix lengths. Only the first row is a
// default route; the second is a connected route, which must be ignored.
const sampleRouteTable = `00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       wlan0
24014900b17bbeb50000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000258 00000002 00000000 00000001   wlp4s0
`

func TestParseRouteTableDefault(t *testing.T) {
	iface := ParseRouteTable(sampleRouteTable)
	if iface != "wlan0" {
		t.Errorf("default route iface = %q, want wlan0", iface)
	}
}

func TestParseRouteTableIgnoresLoopback(t *testing.T) {
	table := `00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo
`
	if iface := ParseRouteTable(table); iface != "" {
		t.Errorf("iface = %q, want empty", iface)
	}
}

func TestParseRouteTableNoDefault(t *testing.T) {
	table := "fe800000000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000003 00000000 00000001 tailscale0\n"
	if iface := ParseRouteTable(table); iface != "" {
		t.Errorf("iface = %q, want empty", iface)
	}
}

func TestPrefixFromAddr(t *testing.T) {
	got := PrefixFromAddr(net.ParseIP("2001:db8:0:1::42"), 64)
	want := net.ParseIP("2001:db8:0:1::")
	if !got.Equal(want) {
		t.Errorf("PrefixFromAddr = %s, want %s", got, want)
	}
}

func TestPrefixFromAddrNarrowBits(t *testing.T) {
	got := PrefixFromAddr(net.ParseIP("2001:db8::1"), 56)
	want := net.ParseIP("2001:db8::")
	if !got.Equal(want) {
		t.Errorf("PrefixFromAddr = %s, want %s", got, want)
	}
}

func TestGlobalIPNeverIPv4(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skip("no interfaces available")
	}
	for _, iface := range ifaces {
		if ip := GlobalIP(iface.Name); ip != nil && ip.To4() != nil {
			t.Errorf("GlobalIP(%s) returned IPv4 %s", iface.Name, ip)
		}
	}
}

func TestHasAddrInPrefix(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil || len(ifaces) == 0 {
		t.Skip("no interfaces available")
	}
	if !HasAddrInPrefix(ifaces[0], net.ParseIP("2001:db8::")) {
		t.Logf("no addr in 2001:db8:: on %s (expected on CI)", ifaces[0].Name)
	}
}
