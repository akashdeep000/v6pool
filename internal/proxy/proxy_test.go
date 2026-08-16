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
