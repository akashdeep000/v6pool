// Package proxy implements the v6pool HTTP and SOCKS5 proxy: source-address
// selection, sticky sessions, rotation and request handling.
package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/akashdeep000/v6pool/internal/claim"
	"github.com/akashdeep000/v6pool/internal/config"
	"github.com/akashdeep000/v6pool/internal/ifaceutil"
	"github.com/akashdeep000/v6pool/internal/metrics"
	"github.com/akashdeep000/v6pool/internal/pool"
)

// SessionSeparator separates a sticky-session token from a username:
// user-session-<token> pins one source address for that token.
const SessionSeparator = "-session-"

const (
	// defaultHostsMax caps tracked per-destination cycles when
	// avoid_hosts_max is absent (<= 0).
	defaultHostsMax = 1024
)

// hostIdleTTL purges per-destination cycles idle this long (memory hygiene
// only; rotation freshness comes from the cycles themselves).
const hostIdleTTL = time.Hour

// Account is a configured credential plus its stream domain (which keeps
// accounts off each other's address sequences).
type Account struct {
	config.Account
	domain uint64
}

type session struct {
	ip     net.IP
	acct   string
	expire time.Time
}

// DialFunc dials an upstream, picking a source address for the account. It is
// a field so tests can substitute a fake connection.
type DialFunc func(ctx context.Context, network, addr string, acct *Account, sessionKey string) (net.Conn, error)

// streamEntry is one exact rotation cycle plus its last use (wall-clock,
// memory hygiene only).
type streamEntry struct {
	stream   *pool.Stream
	lastSeen time.Time
}

// Proxy is a rotating IPv6 HTTP/SOCKS5 proxy.
type Proxy struct {
	cfg      config.Config
	pool     *pool.Pool
	hosts    []net.IP
	accounts map[string]*Account
	claimer  *claim.Claimer
	stats    *metrics.Stats
	dial     DialFunc
	seed     uint64

	mu        sync.Mutex
	sessions  map[string]*session
	streams   map[string]*streamEntry // per (account, destination[, prefix])
	streamMax int
	// brMap/brRing is a small global recent-IP ring: a cross-stream
	// coincidence guard (independent per-destination cycles can still emit
	// the same address). Depth avoid_recent, 0 disables.
	brMap    map[string]struct{}
	brRing   []string
	brIdx    int
	brCap    int
	hostPick atomic.Uint64
}

// New builds a Proxy from a validated config.
func New(cfg *config.Config) (*Proxy, error) {
	seed, err := parseSeed(cfg.PoolSeed)
	if err != nil {
		return nil, err
	}
	p := &Proxy{
		cfg:       *cfg,
		accounts:  make(map[string]*Account, len(cfg.Accounts)),
		sessions:  make(map[string]*session),
		streams:   make(map[string]*streamEntry),
		brMap:     make(map[string]struct{}),
		brCap:     cfg.AvoidRecent,
		streamMax: cfg.AvoidHostsMax,
	}
	if p.streamMax <= 0 {
		p.streamMax = defaultHostsMax
	}
	if cfg.PoolPrefix != "" {
		p.pool = pool.NewWithSeed(cfg.PoolBits, net.ParseIP(cfg.PoolPrefix), seed)
	}
	for _, h := range cfg.PoolHosts {
		p.hosts = append(p.hosts, net.ParseIP(h).To16())
	}
	usernames := make([]string, 0, len(cfg.Accounts))
	for i := range cfg.Accounts {
		a := &Account{Account: cfg.Accounts[i]}
		if p.pool != nil {
			a.domain = p.pool.AccountDomain(uint64(i))
		}
		p.accounts[a.Username] = a
		usernames = append(usernames, a.Username)
	}
	p.stats = metrics.New(usernames)
	p.stats.SeedFP = hex.EncodeToString(uint64Bytes(seed))[:16]
	p.claimer = claim.New(cfg.ClaimIface, cfg.ClaimTTL)
	p.dial = p.dialTarget
	return p, nil
}

// parseSeed decodes an optional hex pool seed; empty means a fresh
// crypto/rand seed per boot so restarts never replay address sequences.
func parseSeed(s string) (uint64, error) {
	if s == "" {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
			uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7]), nil
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return 0, fmt.Errorf("invalid pool_seed %q: %w", s, err)
	}
	if len(raw) == 0 || len(raw) > 8 {
		return 0, fmt.Errorf("invalid pool_seed %q: want 1-16 hex chars", s)
	}
	var v uint64
	for _, b := range raw {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

func uint64Bytes(v uint64) []byte {
	return []byte{
		byte(v >> 56), byte(v >> 48), byte(v >> 40), byte(v >> 32),
		byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v),
	}
}

// Stats exposes the proxy's counter set.
func (p *Proxy) Stats() *metrics.Stats {
	return p.stats
}

// PoolMode describes the active source-selection strategy, for metrics.
func (p *Proxy) PoolMode() string {
	switch {
	case p.cfg.FixedSource != "":
		return "fixed_source"
	case len(p.hosts) > 0:
		return "pool_hosts"
	case p.cfg.AutoPool:
		return "auto_pool"
	case p.cfg.SourceIface != "":
		return "source_iface"
	default:
		return "pool_prefix"
	}
}

// authenticate resolves user to an account, splitting off any sticky-session
// token. Passwords are compared in constant time.
func (p *Proxy) authenticate(user, pass string) (*Account, string) {
	base, sessionKey := splitUsername(user)
	acct, ok := p.accounts[base]
	if !ok {
		return nil, ""
	}
	if !ConstantTimeEqual(acct.Password, pass) {
		return nil, ""
	}
	return acct, sessionKey
}

// splitUsername separates a sticky-session token from the account username.
func splitUsername(user string) (base, sessionKey string) {
	if i := indexOf(user, SessionSeparator); i >= 0 {
		return user[:i], user[i+len(SessionSeparator):]
	}
	return user, ""
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ConstantTimeEqual compares two strings without short-circuiting on length
// mismatch position, to resist timing attacks on credentials and tokens.
func ConstantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// pickSource chooses the source address for a request to destHost.
// Priority: fixed_source, then the interface's live address
// (single-address mode), then the sticky-session address, then the next
// address in the per-destination rotation cycle.
func (p *Proxy) pickSource(acct *Account, sessionKey, destHost string) net.IP {
	if p.cfg.FixedSource != "" {
		return net.ParseIP(p.cfg.FixedSource)
	}
	if p.cfg.SourceIface != "" && !p.cfg.AutoPool {
		if ip := ifaceutil.GlobalIP(p.cfg.SourceIface); ip != nil {
			return ip
		}
	}
	if sessionKey != "" {
		if ip, ok := p.stickyIP(acct, sessionKey, destHost); ok {
			return ip
		}
	}
	return p.freshIP(acct, destHost)
}

// livePrefix derives the pool prefix from the interface's current global
// address, masked to pool_bits. It tracks the interface directly (auto_pool
// mode), or the interface that owns the default route when none is
// configured. Returns nil when no usable address is available.
func (p *Proxy) livePrefix() net.IP {
	iface := p.cfg.SourceIface
	if iface == "" {
		iface = ifaceutil.DefaultRouteIface()
		if iface == "" {
			return nil
		}
	}
	ip := ifaceutil.GlobalIP(iface)
	if ip == nil {
		return nil
	}
	return ifaceutil.PrefixFromAddr(ip, p.cfg.PoolBits)
}

// freshIP picks the next rotating address for destHost. Address-list mode
// round-robins the configured addresses. Sliced accounts use the legacy
// keyed uniform mapping over their range. Full-pool picks advance the
// per-(account, destination) breadth-first cycle, so consecutive picks walk
// fresh aggregates at every prefix level with zero tracking; a small global
// recent-IP ring guards against cross-stream coincidences.
func (p *Proxy) freshIP(acct *Account, destHost string) net.IP {
	if len(p.hosts) > 0 {
		n := p.hostPick.Add(1)
		return p.hosts[n%uint64(len(p.hosts))]
	}
	pl, suffix := p.poolForPick()
	if pl == nil {
		return nil
	}
	if acct.Size > 0 {
		gen := func() net.IP {
			n := p.hostPick.Add(1)
			return pl.IPForAcct(n, acct.domain^tweakHost(destHost), acct.Start, acct.Size)
		}
		return p.backstopGuard(gen(), gen)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.streamLocked(acct, destHost, pl, suffix)
	ip, sk := st.Next()
	p.stats.CycleSkips.Add(uint64(sk))
	ip = p.backstopLocked(ip, func() net.IP {
		ip, sk := st.Next()
		p.stats.CycleSkips.Add(uint64(sk))
		return ip
	})
	p.stats.PicksTotal.Add(1)
	return ip
}

// poolForPick resolves the pool for one pick: the static pool, or a
// per-pick pool over the live prefix in auto_pool mode. The suffix
// distinguishes auto-pool prefixes in stream keys so a roam starts a fresh
// cycle instead of reusing stale positions.
func (p *Proxy) poolForPick() (pl *pool.Pool, suffix string) {
	if !p.cfg.AutoPool {
		return p.pool, ""
	}
	pre := p.livePrefix()
	if pre == nil {
		return nil, ""
	}
	return pool.NewWithSeed(p.cfg.PoolBits, pre, p.seed), "\x00" + pre.String()
}

// streamLocked returns the cycle for (account, destination[, prefix]),
// creating it on demand and evicting the idlest entry over cap. Callers
// must hold p.mu.
func (p *Proxy) streamLocked(acct *Account, destHost string, pl *pool.Pool, suffix string) *pool.Stream {
	key := acct.Username + "\x00" + destHost + suffix
	if e, ok := p.streams[key]; ok {
		e.lastSeen = time.Now()
		return e.stream
	}
	for p.streamMax > 0 && len(p.streams) >= p.streamMax {
		p.evictStreamLocked()
	}
	e := &streamEntry{stream: pl.NewStream(acct.domain, tweakHost(destHost)), lastSeen: time.Now()}
	p.streams[key] = e
	p.stats.HostsCur.Store(int64(len(p.streams)))
	return e.stream
}

// evictStreamLocked drops the least-recently-seen cycle. Callers must hold
// p.mu.
func (p *Proxy) evictStreamLocked() {
	oldest, oldestAt := "", time.Now()
	for k, e := range p.streams {
		if e.lastSeen.Before(oldestAt) || oldest == "" {
			oldest, oldestAt = k, e.lastSeen
		}
	}
	if oldest != "" {
		delete(p.streams, oldest)
	}
}

// backstopGuard records ip in the global recent-IP ring, regenerating via
// next on coincidences (bounded). It is the cross-stream safety net:
// independent per-destination cycles can still emit the same address.
func (p *Proxy) backstopGuard(ip net.IP, next func() net.IP) net.IP {
	if p.brCap <= 0 {
		return ip
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.backstopLocked(ip, next)
}

// backstopLocked is backstopGuard under the caller's lock.
func (p *Proxy) backstopLocked(ip net.IP, next func() net.IP) net.IP {
	if p.brCap <= 0 {
		return ip
	}
	for i := 0; i < 3 && next != nil && p.brHit(ip); i++ {
		ip = next()
	}
	key := ip.String()
	if _, ok := p.brMap[key]; !ok {
		if len(p.brRing) < p.brCap {
			p.brRing = append(p.brRing, key)
		} else if p.brCap > 0 {
			old := p.brRing[p.brIdx%len(p.brRing)]
			delete(p.brMap, old)
			p.brRing[p.brIdx%len(p.brRing)] = key
			p.brIdx++
		}
		p.brMap[key] = struct{}{}
	}
	return ip
}

// brHit reports whether ip is in the recent-IP ring. Callers must hold p.mu.
func (p *Proxy) brHit(ip net.IP) bool {
	_, ok := p.brMap[ip.String()]
	return ok
}

// normalizeHost canonicalizes an upstream host for per-destination cycles:
// lowercase, no trailing dot. IP literals pass through lowercased.
func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// tweakHost hashes a normalized destination into a cycle tweak so each site
// walks an independent rotation.
func tweakHost(destHost string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(destHost))
	return h.Sum64()
}

// stickyIP returns the address pinned to a session token, creating a new
// session when needed. Sessions expire after sticky_ttl_seconds. Fresh
// session addresses advance the destination cycle, so new sessions also
// spread; only repeat visits pin.
func (p *Proxy) stickyIP(acct *Account, sessionKey, destHost string) (net.IP, bool) {
	p.mu.Lock()
	if s, ok := p.sessions[sessionKey]; ok && s.acct == acct.Username && time.Now().Before(s.expire) {
		p.mu.Unlock()
		return s.ip, true
	}
	if s, ok := p.sessions[sessionKey]; ok && time.Now().After(s.expire) {
		p.dropSessionLocked(sessionKey)
	}
	p.mu.Unlock()

	// The lock is dropped before picking a fresh address: freshIP takes
	// p.mu itself for stream lookup and backstop recording.
	ip := p.freshIP(acct, destHost)

	p.mu.Lock()
	if s, ok := p.sessions[sessionKey]; ok && s.acct == acct.Username && time.Now().Before(s.expire) {
		p.mu.Unlock()
		return s.ip, true
	}
	p.sessions[sessionKey] = &session{
		ip:     ip,
		acct:   acct.Username,
		expire: time.Now().Add(time.Duration(p.cfg.StickyTTL) * time.Second),
	}
	p.stats.SessionsCur.Add(1)
	p.stats.SessionsTot.Add(1)
	p.mu.Unlock()
	return ip, true
}

// dropSessionLocked removes a session, keeping the session gauge in sync.
// Callers must hold p.mu.
func (p *Proxy) dropSessionLocked(key string) {
	delete(p.sessions, key)
	p.stats.SessionsCur.Add(-1)
}

// SweepSessions removes expired sticky sessions.
func (p *Proxy) SweepSessions() {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, s := range p.sessions {
		if now.After(s.expire) {
			p.dropSessionLocked(k)
		}
	}
}

// SweepClaims removes idle claimed source addresses. Called periodically.
func (p *Proxy) SweepClaims() {
	p.claimer.Sweep()
}

// SweepStreams purges per-destination cycles idle longer than hostIdleTTL
// and enforces the stream cap. Called periodically; rotation freshness
// comes from the cycles themselves, so sweeping is memory hygiene only.
func (p *Proxy) SweepStreams() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.streams {
		if time.Since(e.lastSeen) > hostIdleTTL {
			delete(p.streams, k)
		}
	}
	for p.streamMax > 0 && len(p.streams) > p.streamMax {
		p.evictStreamLocked()
	}
	p.stats.HostsCur.Store(int64(len(p.streams)))
}

// ensureClaimed binds src to the tether interface when it is not already
// assigned, reporting success/failure to the metrics.
func (p *Proxy) ensureClaimed(src net.IP) {
	if err := p.claimer.EnsureClaimed(src); err != nil {
		p.stats.ClaimsFail.Add(1)
		return
	}
	p.stats.ClaimsOK.Add(1)
}

// logReq emits one structured line per request when log_requests is enabled.
func (p *Proxy) logReq(acct *Account, sessionKey, method, host string, status int, src net.IP, d time.Duration) {
	if !p.cfg.LogRequests {
		return
	}
	slog.Info("req",
		"user", acct.Username, "session", sessionKey,
		"method", method, "host", host,
		"status", status, "src", src,
		"ms", d.Milliseconds(),
	)
}

// dialTarget dials the upstream with a picked source address, preferring IPv6
// and falling back to IPv4. Failures are classified into the dial-error
// metrics.
func (p *Proxy) dialTarget(ctx context.Context, network, addr string, acct *Account, sessionKey string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	src := p.pickSource(acct, sessionKey, normalizeHost(host))
	p.ensureClaimed(src)
	d6 := net.Dialer{
		LocalAddr: &net.TCPAddr{IP: src},
		Timeout:   time.Duration(p.cfg.DialTimeout) * time.Second,
	}
	d4 := net.Dialer{
		Timeout: time.Duration(p.cfg.DialTimeout) * time.Second,
	}

	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil && ip.To16() != nil {
			conn, err := d6.DialContext(ctx, "tcp6", addr)
			if err != nil {
				p.stats.AddDialErr(err)
				return nil, err
			}
			return conn, nil
		}
		conn, err := d4.DialContext(ctx, "tcp4", addr)
		if err != nil {
			p.stats.AddDialErr(err)
			return nil, err
		}
		return conn, nil
	}

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if ip.To4() == nil && ip.To16() != nil {
			conn, err := d6.DialContext(ctx, "tcp6", net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			p.stats.AddDialErr(err)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
	}
	conn, err := d4.DialContext(ctx, "tcp4", addr)
	if err != nil {
		p.stats.AddDialErr(err)
		return nil, err
	}
	return conn, nil
}
