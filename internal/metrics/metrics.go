// Package metrics tracks v6pool counters and renders them as JSON (for the
// human-facing /stats endpoint) or in the Prometheus text exposition format
// (for /metrics). All counters are atomics; no goroutines or timers are
// created, so the overhead is a few increments per request.
package metrics

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"
)

// DialReason buckets the reasons an outbound dial can fail. The classification
// is intentionally coarse: enough to detect a stale pool prefix (almost every
// dial fails with "network unreachable") without burning CPU on parsing.
type DialReason string

const (
	DialUnreachable DialReason = "network_unreachable"
	DialTimeout     DialReason = "timeout"
	DialRefused     DialReason = "connection_refused"
	DialOther       DialReason = "other"
)

// Account holds per-account counters. Instances are created once at startup,
// so plain atomic fields are safe for concurrent use without a lock.
type Account struct {
	HTTPReqs   atomic.Uint64
	SOCKSConns atomic.Uint64
	Rejected   atomic.Uint64
	BytesIn    atomic.Uint64
	BytesOut   atomic.Uint64
}

// Stats is the central counter set for one proxy instance.
type Stats struct {
	Started     time.Time
	HTTPReqs    atomic.Uint64
	SOCKSConns  atomic.Uint64
	ActiveConns atomic.Int64
	Rejected    atomic.Uint64
	BytesIn     atomic.Uint64
	BytesOut    atomic.Uint64
	ClaimsOK    atomic.Uint64
	ClaimsFail  atomic.Uint64
	SessionsCur atomic.Int64
	SessionsTot atomic.Uint64
	DialErrors  map[DialReason]*atomic.Uint64
	Accounts    map[string]*Account // keyed by account username
}

// New returns a Stats with pre-created dial-error buckets and per-account
// counters for the given usernames.
func New(usernames []string) *Stats {
	dial := make(map[DialReason]*atomic.Uint64, 4)
	for _, r := range []DialReason{DialUnreachable, DialTimeout, DialRefused, DialOther} {
		dial[r] = &atomic.Uint64{}
	}
	accounts := make(map[string]*Account, len(usernames))
	for _, u := range usernames {
		accounts[u] = &Account{}
	}
	return &Stats{
		Started:    time.Now(),
		DialErrors: dial,
		Accounts:   accounts,
	}
}

// Account returns the counter set for a username.
func (s *Stats) Account(username string) *Account {
	return s.Accounts[username]
}

// AddDialErr classifies err and bumps the matching reason counter.
func (s *Stats) AddDialErr(err error) {
	s.DialErrors[ClassifyDialErr(err)].Add(1)
}

// ClassifyDialErr buckets a dial error into a DialReason.
func ClassifyDialErr(err error) DialReason {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return DialTimeout
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "network is unreachable"),
		strings.Contains(msg, "no route to host"):
		return DialUnreachable
	case strings.Contains(msg, "connection refused"):
		return DialRefused
	}
	return DialOther
}

// JSON renders the current counters as a JSON-friendly map, in the same shape
// as the original /stats endpoint.
func (s *Stats) JSON() map[string]any {
	return map[string]any{
		"uptime_seconds": int(time.Since(s.Started).Seconds()),
		"accounts":       len(s.Accounts),
		"session_count":  s.SessionsCur.Load(),
		"http_requests":  s.HTTPReqs.Load(),
		"socks5_conns":   s.SOCKSConns.Load(),
		"active_conns":   s.ActiveConns.Load(),
		"bytes_in":       s.BytesIn.Load(),
		"bytes_out":      s.BytesOut.Load(),
		"rejected":       s.Rejected.Load(),
		"claims_ok":      s.ClaimsOK.Load(),
		"claims_failed":  s.ClaimsFail.Load(),
		"sessions_total": s.SessionsTot.Load(),
		"dial_errors":    s.dialErrCounts(),
	}
}

// WritePrometheus renders the counters in the Prometheus text exposition
// format (https://prometheus.io/docs/instrumenting/exposition_formats/).
// version and poolMode are attached to the v6pool_info gauge for alerting
// context. Each metric family is declared once (HELP/TYPE), followed by its
// samples, so the output parses cleanly with duplicate-label families.
func (s *Stats) WritePrometheus(w io.Writer, version, poolMode string) {
	family := func(name, typ, help string, samples ...string) {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
		for _, smp := range samples {
			_, _ = fmt.Fprintln(w, smp)
		}
	}
	family("v6pool_http_requests_total", "counter", "HTTP proxy requests.",
		fmt.Sprintf("v6pool_http_requests_total %d", s.HTTPReqs.Load()))
	family("v6pool_socks5_connections_total", "counter", "SOCKS5 connections.",
		fmt.Sprintf("v6pool_socks5_connections_total %d", s.SOCKSConns.Load()))
	family("v6pool_active_connections", "gauge", "Relayed connections in flight.",
		fmt.Sprintf("v6pool_active_connections %d", s.ActiveConns.Load()))
	family("v6pool_rejected_auth_total", "counter", "Requests rejected for bad credentials.",
		fmt.Sprintf("v6pool_rejected_auth_total %d", s.Rejected.Load()))
	family("v6pool_bytes_in_total", "counter", "Bytes received from upstreams.",
		fmt.Sprintf("v6pool_bytes_in_total %d", s.BytesIn.Load()))
	family("v6pool_bytes_out_total", "counter", "Bytes sent to upstreams.",
		fmt.Sprintf("v6pool_bytes_out_total %d", s.BytesOut.Load()))
	family("v6pool_claims_total", "counter", "Source addresses claimed on an interface.",
		fmt.Sprintf("v6pool_claims_total{result=\"success\"} %d", s.ClaimsOK.Load()))
	family("v6pool_claims_failed_total", "counter", "Failed source address claims.",
		fmt.Sprintf("v6pool_claims_failed_total %d", s.ClaimsFail.Load()))
	family("v6pool_sessions_current", "gauge", "Active sticky sessions.",
		fmt.Sprintf("v6pool_sessions_current %d", s.SessionsCur.Load()))
	family("v6pool_sessions_created_total", "counter", "Sticky sessions created.",
		fmt.Sprintf("v6pool_sessions_created_total %d", s.SessionsTot.Load()))
	family("v6pool_uptime_seconds", "gauge", "Seconds since start.",
		fmt.Sprintf("v6pool_uptime_seconds %g", time.Since(s.Started).Seconds()))
	family("v6pool_info", "gauge", "Static build and pool mode information.",
		fmt.Sprintf("v6pool_info{version=%q,pool_mode=%q} 1", version, poolMode))
	family("v6pool_dial_errors_total", "counter", "Outbound dial failures by reason.",
		s.dialErrorSamples()...)
	family("v6pool_account_http_requests_total", "counter", "HTTP proxy requests per account.",
		s.accountSamples("v6pool_account_http_requests_total", func(a *Account) uint64 { return a.HTTPReqs.Load() })...)
	family("v6pool_account_socks5_connections_total", "counter", "SOCKS5 connections per account.",
		s.accountSamples("v6pool_account_socks5_connections_total", func(a *Account) uint64 { return a.SOCKSConns.Load() })...)
	family("v6pool_account_rejected_auth_total", "counter", "Failed authentication per account.",
		s.accountSamples("v6pool_account_rejected_auth_total", func(a *Account) uint64 { return a.Rejected.Load() })...)
	family("v6pool_account_bytes_in_total", "counter", "Bytes received per account.",
		s.accountSamples("v6pool_account_bytes_in_total", func(a *Account) uint64 { return a.BytesIn.Load() })...)
	family("v6pool_account_bytes_out_total", "counter", "Bytes sent per account.",
		s.accountSamples("v6pool_account_bytes_out_total", func(a *Account) uint64 { return a.BytesOut.Load() })...)
}

func (s *Stats) dialErrorSamples() []string {
	out := make([]string, 0, len(s.DialErrors))
	for _, r := range []DialReason{DialUnreachable, DialTimeout, DialRefused, DialOther} {
		out = append(out, fmt.Sprintf("v6pool_dial_errors_total{reason=%q} %d", r, s.DialErrors[r].Load()))
	}
	return out
}

func (s *Stats) accountSamples(name string, load func(*Account) uint64) []string {
	out := make([]string, 0, len(s.Accounts))
	for username, a := range s.Accounts {
		out = append(out, fmt.Sprintf("%s{account=%q} %d", name, username, load(a)))
	}
	return out
}

func (s *Stats) dialErrCounts() map[string]uint64 {
	out := make(map[string]uint64, len(s.DialErrors))
	for r, c := range s.DialErrors {
		out[string(r)] = c.Load()
	}
	return out
}
