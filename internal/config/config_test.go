package config

import (
	"os"
	"path/filepath"
	"strconv"
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

func TestLoadListenersEnabledByDefault(t *testing.T) {
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
	if !cfg.EnableHTTP || !cfg.EnableSOCKS5 || !cfg.EnableStats {
		t.Errorf("listeners should default to enabled: %+v", cfg)
	}
}

func TestLoadListenersCanBeDisabled(t *testing.T) {
	path := writeConfig(t, `
pool_prefix: "2001:db8::"
enable_http: false
enable_stats: false
accounts:
  - username: u
    password: p
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableHTTP {
		t.Error("enable_http: false not honored")
	}
	if !cfg.EnableSOCKS5 {
		t.Error("socks5 listener should stay enabled")
	}
	if cfg.EnableStats {
		t.Error("enable_stats: false not honored")
	}
}

func TestLoadNullEnableKeyTreatedAsAbsent(t *testing.T) {
	path := writeConfig(t, `
pool_prefix: "2001:db8::"
enable_socks5: null
accounts:
  - username: u
    password: p
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableSOCKS5 {
		t.Error("null enable_socks5 should default to enabled")
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
		{"bits too large", "pool_prefix: 2001:db8::\npool_bits: 129\naccounts:\n  - username: u\n    password: p\n"},
		{"negative bits", "pool_prefix: 2001:db8::\npool_bits: -1\naccounts:\n  - username: u\n    password: p\n"},
		{"removed avoid_levels knob", "pool_prefix: \"2001:db8::\"\navoid_levels: [56]\naccounts:\n  - username: u\n    password: p\n"},
		{"removed avoid_per_host knob", "pool_prefix: \"2001:db8::\"\navoid_per_host: 32\naccounts:\n  - username: u\n    password: p\n"},
		{"bad seed", "pool_prefix: \"2001:db8::\"\npool_seed: zzzz\naccounts:\n  - username: u\n    password: p\n"},
		{"long seed", "pool_prefix: \"2001:db8::\"\npool_seed: 00112233445566778899\naccounts:\n  - username: u\n    password: p\n"},
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

func TestLoadShortAndLongPools(t *testing.T) {
	for _, bits := range []int{32, 48, 56, 64, 96, 120, 128} {
		path := writeConfig(t, `
pool_prefix: "2001:db8::"
pool_bits: `+strconv.Itoa(bits)+`
accounts:
  - username: u
    password: p
`)
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("pool_bits %d rejected: %v", bits, err)
		}
		if cfg.PoolBits != bits {
			t.Errorf("PoolBits = %d, want %d", cfg.PoolBits, bits)
		}
	}
}

func TestLoadRotationKnobs(t *testing.T) {
	path := writeConfig(t, `
pool_prefix: "2a01:d0:b081::"
pool_bits: 48
avoid_recent: 1024
avoid_hosts_max: 100
pool_seed: deadbeef
accounts:
  - username: u
    password: p
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AvoidRecent != 1024 || cfg.AvoidHostsMax != 100 || cfg.PoolSeed != "deadbeef" {
		t.Errorf("rotation knobs = %+v", cfg)
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
