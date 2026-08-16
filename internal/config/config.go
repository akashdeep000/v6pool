// Package config defines the v6pool configuration schema, its defaults and
// validation.
package config

import (
	"bytes"
	"fmt"
	"net"
	"os"

	"gopkg.in/yaml.v3"
)

// Defaults applied when the corresponding keys are absent from the config.
const (
	DefaultHTTPListen   = ":3128" // conventional HTTP proxy port (Squid)
	DefaultSOCKS5Listen = ":1080"
	DefaultPoolBits     = 64
	DefaultStickyTTL    = 300
	DefaultAvoidRecent  = 128
	DefaultDialTimeout  = 15
	DefaultClaimTTL     = 300
)

// Account is a named credential with an optional slice of the address pool.
// When Size > 0, source addresses are drawn from [Start, Start+Size) of the
// pool; otherwise the full pool is used.
type Account struct {
	Name     string `yaml:"name"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Start    uint64 `yaml:"start"`
	Size     uint64 `yaml:"size"`
}

// Config is the v6pool configuration.
type Config struct {
	HTTPListen   string    `yaml:"http_listen"`
	SOCKS5Listen string    `yaml:"socks5_listen"`
	StatsListen  string    `yaml:"stats_listen"`
	StatsToken   string    `yaml:"stats_token"`
	PoolPrefix   string    `yaml:"pool_prefix"`
	PoolBits     int       `yaml:"pool_bits"`
	PoolHosts    []string  `yaml:"pool_hosts"`
	FixedSource  string    `yaml:"fixed_source"`
	SourceIface  string    `yaml:"source_iface"`
	AutoPool     bool      `yaml:"auto_pool"`
	Freebind     bool      `yaml:"freebind"`
	ClaimIface   string    `yaml:"claim_iface"`
	ClaimTTL     int       `yaml:"claim_ttl_seconds"`
	LogRequests  bool      `yaml:"log_requests"`
	StickyTTL    int       `yaml:"sticky_ttl_seconds"`
	AvoidRecent  int       `yaml:"avoid_recent"`
	DialTimeout  int       `yaml:"dial_timeout_seconds"`
	Accounts     []Account `yaml:"accounts"`
}

// Load reads, parses and validates the configuration file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Track which keys the file actually set, so an explicit empty
	// http_listen/socks5_listen can disable a listener instead of being
	// replaced by the default port.
	var present map[string]any
	if err := yaml.Unmarshal(data, &present); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	_, hasHTTP := present["http_listen"]
	_, hasSOCKS := present["socks5_listen"]
	cfg.applyDefaults(hasHTTP, hasSOCKS)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyDefaults fills in defaults for keys absent from the config. An
// explicitly empty http_listen or socks5_listen disables that listener.
func (c *Config) applyDefaults(hasHTTP, hasSOCKS bool) {
	if !hasHTTP && c.HTTPListen == "" {
		c.HTTPListen = DefaultHTTPListen
	}
	if !hasSOCKS && c.SOCKS5Listen == "" {
		c.SOCKS5Listen = DefaultSOCKS5Listen
	}
	if c.PoolBits == 0 {
		c.PoolBits = DefaultPoolBits
	}
	if c.StickyTTL == 0 {
		c.StickyTTL = DefaultStickyTTL
	}
	if c.AvoidRecent == 0 {
		c.AvoidRecent = DefaultAvoidRecent
	}
	if c.DialTimeout == 0 {
		c.DialTimeout = DefaultDialTimeout
	}
	if c.ClaimTTL == 0 {
		c.ClaimTTL = DefaultClaimTTL
	}
}

func (c *Config) validate() error {
	if c.PoolPrefix == "" && len(c.PoolHosts) == 0 && !c.AutoPool && c.FixedSource == "" {
		return fmt.Errorf("pool_prefix, pool_hosts, auto_pool or fixed_source required")
	}
	if c.PoolPrefix != "" {
		prefix := net.ParseIP(c.PoolPrefix)
		if prefix == nil || prefix.To16() == nil {
			return fmt.Errorf("invalid pool_prefix %q", c.PoolPrefix)
		}
	}
	for _, h := range c.PoolHosts {
		ip := net.ParseIP(h)
		if ip == nil || ip.To16() == nil || ip.To4() != nil {
			return fmt.Errorf("invalid pool_hosts entry %q", h)
		}
	}
	if len(c.Accounts) == 0 {
		return fmt.Errorf("no accounts configured")
	}
	for i := range c.Accounts {
		if c.Accounts[i].Username == "" {
			return fmt.Errorf("account %d missing username", i)
		}
	}
	if c.StickyTTL < 0 || c.AvoidRecent < 0 || c.DialTimeout < 0 || c.ClaimTTL < 0 {
		return fmt.Errorf("sticky_ttl_seconds, avoid_recent, dial_timeout_seconds and claim_ttl_seconds must be non-negative")
	}
	return nil
}
