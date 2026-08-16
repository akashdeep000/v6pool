//go:build linux

package proxy

import (
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// nonlocalBind reports whether the host allows binding to non-local IPv6
// addresses globally (net.ipv6.ip_nonlocal_bind). v6pool's installer sets it,
// which would mask the freebind difference below.
func nonlocalBind() bool {
	b, err := os.ReadFile("/proc/sys/net/ipv6/ip_nonlocal_bind")
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// TestFreebindBind proves that IP_FREEBIND changes the bind outcome for
// unassigned source addresses: without it an unprivileged dial fails at
// bind() (EADDRNOTAVAIL), with it the bind succeeds and the failure moves to
// connect/unreachable — which is all an Android/Termux process without root
// needs for rotation on a network that routes the whole prefix.
func TestFreebindBind(t *testing.T) {
	src := net.ParseIP("2001:db8:ffff::99") // documentation prefix, never local

	if !nonlocalBind() {
		d := net.Dialer{
			LocalAddr: &net.TCPAddr{IP: src},
			Timeout:   3 * time.Second,
		}
		_, err := d.Dial("tcp6", "[::1]:1")
		if !errors.Is(err, syscall.EADDRNOTAVAIL) {
			t.Fatalf("without freebind: got %v, want EADDRNOTAVAIL", err)
		}
	}

	d := net.Dialer{
		LocalAddr: &net.TCPAddr{IP: src},
		Timeout:   3 * time.Second,
		Control:   freebindControl,
	}
	_, err := d.Dial("tcp6", "[::1]:1")
	if err == nil {
		t.Fatal("dial unexpectedly succeeded")
	}
	if errors.Is(err, syscall.EADDRNOTAVAIL) {
		t.Fatalf("with freebind: bind still failed with %v", err)
	}
}
