package proxy

import (
	"net"
	"testing"
	"time"

	"github.com/akashdeep000/v6pool/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		PoolPrefix:  "2001:db8::",
		PoolBits:    64,
		StickyTTL:   300,
		AvoidRecent: 128,
		DialTimeout: 15,
		ClaimTTL:    300,
		Accounts: []config.Account{
			{Username: "u", Password: "p"},
		},
	}
}

func newTestProxy(t *testing.T) *Proxy {
	t.Helper()
	p, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAuthenticate(t *testing.T) {
	p := newTestProxy(t)
	acct, key := p.authenticate("u", "p")
	if acct == nil || key != "" {
		t.Fatalf("authenticate ok user: acct=%v key=%q", acct, key)
	}
	if acct, _ := p.authenticate("u", "wrong"); acct != nil {
		t.Fatal("wrong password accepted")
	}
	if acct, _ := p.authenticate("nobody", "p"); acct != nil {
		t.Fatal("unknown user accepted")
	}
}

func TestAuthenticateSessionToken(t *testing.T) {
	p := newTestProxy(t)
	acct, key := p.authenticate("u-session-abc123", "p")
	if acct == nil || key != "abc123" {
		t.Fatalf("session token split: acct=%v key=%q", acct, key)
	}
}

func TestConstantTimeEqual(t *testing.T) {
	if !ConstantTimeEqual("secret", "secret") {
		t.Error("equal strings rejected")
	}
	if ConstantTimeEqual("secret", "secret2") {
		t.Error("different strings accepted")
	}
}

func TestRotationDistinct(t *testing.T) {
	p := newTestProxy(t)
	acct := p.accounts["u"]
	seen := make(map[string]bool)
	for i := 0; i < 10; i++ {
		ip := p.pickSource(acct, "")
		if ip == nil {
			t.Fatal("nil source picked")
		}
		seen[ip.String()] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected rotation, got single address %v", seen)
	}
}

func TestStickySessionPinsAddress(t *testing.T) {
	p := newTestProxy(t)
	acct := p.accounts["u"]
	first := p.pickSource(acct, "tok1")
	for i := 0; i < 20; i++ {
		if got := p.pickSource(acct, "tok1"); !got.Equal(first) {
			t.Fatalf("sticky session drifted: %s then %s", first, got)
		}
	}
	other := p.pickSource(acct, "tok2")
	if other.Equal(first) {
		t.Fatalf("distinct tokens pinned to same address %s", other)
	}
}

func TestStickySessionExpiry(t *testing.T) {
	p := newTestProxy(t)
	p.cfg.StickyTTL = 0
	acct := p.accounts["u"]
	first := p.pickSource(acct, "tok1")
	p.sessions["tok1"].expire = time.Now().Add(-time.Second)
	if got := p.pickSource(acct, "tok1"); got.Equal(first) {
		t.Fatal("expired session not replaced")
	}
	p.SweepSessions()
	if len(p.sessions) != 0 {
		t.Fatalf("sweep left %d sessions", len(p.sessions))
	}
}

func TestAvoidRecent(t *testing.T) {
	p := newTestProxy(t)
	acct := p.accounts["u"]
	p.cfg.AvoidRecent = 2
	var last net.IP
	for i := 0; i < 5; i++ {
		ip := p.pickSource(acct, "")
		if i > 0 && ip.Equal(last) {
			t.Fatalf("repeated address %s in avoid window", ip)
		}
		last = ip
	}
}

func TestAccountRangeRestriction(t *testing.T) {
	cfg := testConfig()
	cfg.Accounts = []config.Account{{Username: "u", Password: "p", Start: 100, Size: 4}}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	acct := p.accounts["u"]
	lo := binaryLow64(p.pickSource(acct, ""))
	if lo < 100 || lo >= 104 {
		t.Fatalf("address outside account range: %x", lo)
	}
}

func TestPoolMode(t *testing.T) {
	p := newTestProxy(t)
	if got := p.PoolMode(); got != "pool_prefix" {
		t.Errorf("PoolMode = %q", got)
	}
	cfg := testConfig()
	cfg.AutoPool = true
	cfg.SourceIface = "wlan0"
	p, _ = New(cfg)
	if got := p.PoolMode(); got != "auto_pool" {
		t.Errorf("PoolMode = %q", got)
	}
}

func binaryLow64(ip net.IP) uint64 {
	b := ip.To16()
	return uint64(b[8])<<56 | uint64(b[9])<<48 | uint64(b[10])<<40 | uint64(b[11])<<32 |
		uint64(b[12])<<24 | uint64(b[13])<<16 | uint64(b[14])<<8 | uint64(b[15])
}

func TestRandomIPFallsBackToConfiguredPool(t *testing.T) {
	cfg := testConfig()
	cfg.AutoPool = true
	cfg.SourceIface = "no-such-iface0"
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ip := p.randomIP(p.accounts["u"])
	mask := net.CIDRMask(cfg.PoolBits, 128)
	if !ip.Mask(mask).Equal(net.ParseIP("2001:db8::").Mask(mask)) {
		t.Errorf("randomIP = %v, want address in configured 2001:db8::/64", ip)
	}
}

func TestRandomIPUsesLearnedPrefix(t *testing.T) {
	cfg := testConfig()
	cfg.AutoPool = true
	cfg.SourceIface = "no-such-iface0"
	cfg.PoolPrefix = ""
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.learned = net.ParseIP("2606:4700:4700::")
	p.learnedAt = time.Now()
	p.mu.Unlock()
	ip := p.randomIP(p.accounts["u"])
	mask := net.CIDRMask(cfg.PoolBits, 128)
	if !ip.Mask(mask).Equal(net.ParseIP("2606:4700:4700::").Mask(mask)) {
		t.Errorf("randomIP = %v, want address in learned 2606:4700:4700::/64", ip)
	}
}

func TestLearnedPoolExpires(t *testing.T) {
	cfg := testConfig()
	cfg.AutoPool = true
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.learned = net.ParseIP("2606:4700:4700::")
	p.learnedAt = time.Now().Add(-2 * learnedTTL)
	p.mu.Unlock()
	if pl := p.learnedPool(); pl != nil {
		t.Fatal("stale learned prefix still served")
	}
}

type fakeLocalConn struct {
	net.Conn
	local net.Addr
}

func (f fakeLocalConn) LocalAddr() net.Addr { return f.local }

func TestMaybeLearn(t *testing.T) {
	cfg := testConfig()
	cfg.AutoPool = true
	cfg.PoolPrefix = ""
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	conn := fakeLocalConn{local: &net.TCPAddr{
		IP:   net.ParseIP("2401:4900:b78a:ab11:f8f1:2ff:fe17:c523"),
		Port: 40000,
	}}
	p.maybeLearn(nil, conn)
	p.mu.Lock()
	got := p.learned
	p.mu.Unlock()
	if !got.Equal(net.ParseIP("2401:4900:b78a:ab11::")) {
		t.Errorf("learned = %v, want 2401:4900:b78a:ab11::", got)
	}

	linkLocal := fakeLocalConn{local: &net.TCPAddr{
		IP: net.ParseIP("fe80::1"), Port: 40000,
	}}
	p.maybeLearn(nil, linkLocal)
	p.mu.Lock()
	got2 := p.learned
	p.mu.Unlock()
	if !got2.Equal(net.ParseIP("2401:4900:b78a:ab11::")) {
		t.Errorf("link-local local addr overwrote learned prefix")
	}
}
