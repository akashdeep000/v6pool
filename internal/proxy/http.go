package proxy

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// httpRequestTimeout bounds a single forwarded HTTP request.
const httpRequestTimeout = 120 * time.Second

// idleRelay is the max time a relayed connection may sit without traffic in
// either direction; prevents leaked goroutines when a peer goes silent.
const idleRelay = 5 * time.Minute

// srcRecorder carries the picked source address through the transport to the
// request logger.
type srcRecorder struct{ ip net.IP }

type srcRecKeyType struct{}

var srcRecKey srcRecKeyType

// Handler returns the HTTP proxy handler (forwarding proxy + CONNECT).
func (p *Proxy) Handler() http.Handler {
	return http.HandlerFunc(p.handleHTTP)
}

func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	p.stats.HTTPReqs.Add(1)
	user, pass, ok := parseBasicAuth(r.Header.Get("Proxy-Authorization"))
	if !ok {
		p.reject(w)
		return
	}
	acct, sessionKey := p.authenticate(user, pass)
	if acct == nil {
		p.reject(w)
		return
	}

	if r.Method == http.MethodConnect {
		p.handleCONNECT(w, r, acct, sessionKey)
		return
	}

	if r.URL.Scheme == "" || r.URL.Host == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), httpRequestTimeout)
	defer cancel()

	rec := &srcRecorder{}
	ctx = context.WithValue(ctx, srcRecKey, rec)

	out := r.Clone(ctx)
	out.Header.Del("Proxy-Authorization")
	out.RequestURI = ""

	tr := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return p.dial(ctx, network, addr, acct, sessionKey)
		},
		MaxIdleConns:    0,
		IdleConnTimeout: 0,
	}
	defer tr.CloseIdleConnections()

	resp, err := tr.RoundTrip(out)
	if err != nil {
		p.logReq(acct, sessionKey, r.Method, r.Host, 502, rec.ip, time.Since(start))
		var ne net.Error
		switch {
		case errors.As(err, &ne) && ne.Timeout():
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
		case isNetErr(err):
			http.Error(w, "bad gateway", http.StatusBadGateway)
		default:
			http.Error(w, "bad gateway", http.StatusInternalServerError)
		}
		return
	}
	defer func() { _ = resp.Body.Close() }()

	h := w.Header()
	for k, vv := range resp.Header {
		for _, v := range vv {
			h.Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	p.logReq(acct, sessionKey, r.Method, r.Host, resp.StatusCode, rec.ip, time.Since(start))
}

func (p *Proxy) reject(w http.ResponseWriter) {
	p.stats.Rejected.Add(1)
	w.Header().Set("Proxy-Authenticate", `Basic realm="v6pool"`)
	w.WriteHeader(http.StatusProxyAuthRequired)
}

func (p *Proxy) handleCONNECT(w http.ResponseWriter, r *http.Request, acct *Account, sessionKey string) {
	start := time.Now()
	dst := r.Host
	if dst == "" {
		dst = r.URL.Host
	}
	client, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
	}
	defer func() { _ = client.Close() }()

	up, err := p.dial(context.Background(), "tcp", dst, acct, sessionKey)
	if err != nil {
		p.logReq(acct, sessionKey, "CONNECT", dst, 502, nil, time.Since(start))
		_, _ = client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer func() { _ = up.Close() }()

	src, _, _ := net.SplitHostPort(up.LocalAddr().String())
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	p.logReq(acct, sessionKey, "CONNECT", dst, 200, net.ParseIP(src), time.Since(start))
	p.relay(client, up)
}

// keepAlive keeps an idle deadline on conn so a silent peer cannot pin the
// relay forever, while active streams refresh it and transfer uninterrupted.
func keepAlive(conn net.Conn, idle time.Duration) func() {
	_ = conn.SetDeadline(time.Now().Add(idle))
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(idle / 4)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				_ = conn.SetDeadline(time.Now().Add(idle))
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop) }
}

// relay pipes two connections until either side closes, tracking bytes and
// active-connection gauges.
func (p *Proxy) relay(a, b net.Conn) {
	p.stats.ActiveConns.Add(1)
	defer p.stats.ActiveConns.Add(-1)
	stopA := keepAlive(a, idleRelay)
	stopB := keepAlive(b, idleRelay)
	defer stopA()
	defer stopB()
	done := make(chan struct{}, 2)
	go func() {
		n, _ := io.Copy(b, a)
		p.stats.BytesOut.Add(uint64(n))
		if tcp, ok := b.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		n, _ := io.Copy(a, b)
		p.stats.BytesIn.Add(uint64(n))
		if tcp, ok := a.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	if tcp, ok := a.(*net.TCPConn); ok {
		_ = tcp.CloseRead()
	}
	<-done
}

func parseBasicAuth(h string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if !strings.HasPrefix(h, prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	s := string(raw)
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

func isNetErr(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr)
}
