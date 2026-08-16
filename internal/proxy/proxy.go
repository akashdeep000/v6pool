// Package proxy implements the v6pool HTTP and SOCKS5 proxy: source-address
// selection, sticky sessions, rotation and request handling.
package proxy

import (
	"context"
	"log/slog"
	"net"
	"sync"
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

// avoidWindow is how long a picked address is skipped for rotation.
const avoidWindow = 10 * time.Second

// Account is a configured credential plus its runtime pick counter.
type Account struct {
	config.Account
	counter uint64
}

type session struct {
	ip     net.IP
	acct   string
	expire time.Time
}

// DialFunc dials an upstream, picking a source address for the account. It is
// a field so tests can substitute a fake connection.
type DialFunc func(ctx context.Context, network, addr string, acct *Account, sessionKey string) (net.Conn, error)

// Proxy is a rotating IPv6 HTTP/SOCKS5 proxy.
type Proxy struct {
	cfg      config.Config
	pool     *pool.Pool
	hosts    []net.IP
	accounts map[string]*Account
	claimer  *claim.Claimer
	stats    *metrics.Stats
	dial     DialFunc

	mu        sync.Mutex
	sessions  map[string]*session
	avoid     map[string]time.Time
	avoidRing []string
	avoidIdx  int
}

// New builds a Proxy from a validated config.
func New(cfg *config.Config) (*Proxy, error) {
	p := &Proxy{
		cfg:      *cfg,
		accounts: make(map[string]*Account, len(cfg.Accounts)),
		sessions: make(map[string]*session),
		avoid:    make(map[string]time.Time),
	}
	if cfg.PoolPrefix != "" {
		p.pool = pool.New(cfg.PoolBits, net.ParseIP(cfg.PoolPrefix))
	}
	for _, h := range cfg.PoolHosts {
		p.hosts = append(p.hosts, net.ParseIP(h).To16())
	}
	usernames := make([]string, 0, len(cfg.Accounts))
	for i := range cfg.Accounts {
		a := &Account{Account: cfg.Accounts[i]}
		p.accounts[a.Username] = a
		usernames = append(usernames, a.Username)
	}
	p.stats = metrics.New(usernames)
	p.claimer = claim.New(cfg.ClaimIface, cfg.ClaimTTL)
	p.dial = p.dialTarget
	return p, nil
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

// pickSource chooses the source address for a request. Priority:
// fixed_source, then the interface's live address (single-address mode),
// then the sticky-session address, then a fresh rotating address.
func (p *Proxy) pickSource(acct *Account, sessionKey string) net.IP {
	if p.cfg.FixedSource != "" {
		return net.ParseIP(p.cfg.FixedSource)
	}
	if p.cfg.SourceIface != "" && !p.cfg.AutoPool {
		if ip := ifaceutil.GlobalIP(p.cfg.SourceIface); ip != nil {
			return ip
		}
	}
	if sessionKey != "" {
		if ip, ok := p.stickyIP(acct, sessionKey); ok {
			return ip
		}
	}
	ip := p.randomIP(acct)
	p.remember(ip)
	return ip
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

// randomIP picks the next rotating address: from the pool_hosts list when
// configured, otherwise from the (possibly auto-derived) prefix pool.
func (p *Proxy) randomIP(acct *Account) net.IP {
	p.mu.Lock()
	acct.counter++
	seq := acct.counter
	p.mu.Unlock()
	if len(p.hosts) > 0 {
		return p.hosts[seq%uint64(len(p.hosts))]
	}
	pl := p.pool
	if p.cfg.AutoPool {
		pre := p.livePrefix()
		if pre == nil {
			return nil
		}
		pl = pool.New(p.cfg.PoolBits, pre)
	}
	var ip net.IP
	for try := 0; try < 8; try++ {
		ip = pl.IPFor(seq, acct.Start, acct.Size)
		if !p.recentlyUsed(ip) {
			break
		}
		seq += 0x9E3779B97F4A7C15
	}
	return ip
}

// recentlyUsed reports whether ip was picked within the avoid window.
func (p *Proxy) recentlyUsed(ip net.IP) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.avoid[ip.String()]
	return ok && time.Since(t) < avoidWindow
}

// remember marks ip as recently used, capping the set at avoid_recent entries
// in a ring so memory stays bounded.
func (p *Proxy) remember(ip net.IP) {
	if p.cfg.AvoidRecent <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key := ip.String()
	if _, ok := p.avoid[key]; ok {
		p.avoid[key] = time.Now()
		return
	}
	if len(p.avoidRing) < p.cfg.AvoidRecent {
		p.avoidRing = append(p.avoidRing, key)
		p.avoid[key] = time.Now()
		return
	}
	old := p.avoidRing[p.avoidIdx%len(p.avoidRing)]
	delete(p.avoid, old)
	p.avoidRing[p.avoidIdx%len(p.avoidRing)] = key
	p.avoidIdx++
	p.avoid[key] = time.Now()
}

// stickyIP returns the address pinned to a session token, creating a new
// session when needed. Sessions expire after sticky_ttl_seconds.
func (p *Proxy) stickyIP(acct *Account, sessionKey string) (net.IP, bool) {
	p.mu.Lock()
	if s, ok := p.sessions[sessionKey]; ok && s.acct == acct.Username && time.Now().Before(s.expire) {
		p.mu.Unlock()
		return s.ip, true
	}
	if s, ok := p.sessions[sessionKey]; ok && time.Now().After(s.expire) {
		p.dropSessionLocked(sessionKey)
	}
	p.mu.Unlock()

	// The lock is dropped before picking a fresh address: randomIP consults
	// the avoid ring, which needs p.mu itself.
	ip := p.randomIP(acct)

	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.sessions[sessionKey]; ok && s.acct == acct.Username && time.Now().Before(s.expire) {
		return s.ip, true
	}
	p.sessions[sessionKey] = &session{
		ip:     ip,
		acct:   acct.Username,
		expire: time.Now().Add(time.Duration(p.cfg.StickyTTL) * time.Second),
	}
	p.stats.SessionsCur.Add(1)
	p.stats.SessionsTot.Add(1)
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
	src := p.pickSource(acct, sessionKey)
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
