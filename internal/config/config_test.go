package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	path := writeConfig(t, `
pool_prefix: "2001:db8::"
accounts:
  - username: u
    password: p
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPListen != ":3128" {
		t.Errorf("HTTPListen = %q, want :3128", cfg.HTTPListen)
	}
	if cfg.SOCKS5Listen != ":1080" {
		t.Errorf("SOCKS5Listen = %q, want :1080", cfg.SOCKS5Listen)
	}
	if cfg.PoolBits != 64 {
		t.Errorf("PoolBits = %d, want 64", cfg.PoolBits)
	}
	if cfg.StickyTTL != 300 || cfg.AvoidRecent != 128 || cfg.DialTimeout != 15 || cfg.ClaimTTL != 300 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadKnownFieldsRejectsTypos(t *testing.T) {
	path := writeConfig(t, `
pool_prefix: "2001:db8::"
pool_bits: 64
accoutns:
  - username: u
    password: p
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestLoadValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no pool", "accounts:\n  - username: u\n    password: p\n"},
		{"no accounts", "pool_prefix: 2001:db8::\n"},
		{"bad prefix", "pool_prefix: not-an-ip\naccounts:\n  - username: u\n    password: p\n"},
		{"ipv4 host", "pool_hosts:\n  - 192.0.2.1\naccounts:\n  - username: u\n    password: p\n"},
		{"empty username", "pool_prefix: 2001:db8::\naccounts:\n  - password: p\n"},
		{"negative ttl", "pool_prefix: 2001:db8::\nsticky_ttl_seconds: -1\naccounts:\n  - username: u\n    password: p\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.body)
			if _, err := Load(path); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadAutoPool(t *testing.T) {
	path := writeConfig(t, `
auto_pool: true
source_iface: eth0
accounts:
  - username: u
    password: p
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AutoPool {
		t.Error("AutoPool not set")
	}
	if cfg.SourceIface != "eth0" {
		t.Errorf("SourceIface = %q, want eth0", cfg.SourceIface)
	}
}

func TestLoadFixedSource(t *testing.T) {
	path := writeConfig(t, `
fixed_source: "2001:db8::1"
accounts:
  - username: u
    password: p
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FixedSource != "2001:db8::1" {
		t.Errorf("FixedSource = %q", cfg.FixedSource)
	}
}

func TestLoadFreebind(t *testing.T) {
	path := writeConfig(t, `
pool_prefix: "2001:db8:1:2::"
freebind: true
accounts:
  - username: u
    password: p
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Freebind {
		t.Error("Freebind not set")
	}
}

func TestLoadDisablesListenersWithEmptyValue(t *testing.T) {
	path := writeConfig(t, `
http_listen: ""
pool_prefix: "2001:db8::"
accounts:
  - username: u
    password: p
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPListen != "" {
		t.Errorf("HTTPListen = %q, want \"\" (disabled)", cfg.HTTPListen)
	}
	if cfg.SOCKS5Listen != ":1080" {
		t.Errorf("SOCKS5Listen = %q, want default :1080", cfg.SOCKS5Listen)
	}
}

func TestLoadDisablesSOCKS5(t *testing.T) {
	path := writeConfig(t, `
socks5_listen: ""
pool_prefix: "2001:db8::"
accounts:
  - username: u
    password: p
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SOCKS5Listen != "" {
		t.Errorf("SOCKS5Listen = %q, want \"\" (disabled)", cfg.SOCKS5Listen)
	}
	if cfg.HTTPListen != ":3128" {
		t.Errorf("HTTPListen = %q, want default :3128", cfg.HTTPListen)
	}
}
