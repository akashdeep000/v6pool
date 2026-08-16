package metrics

import (
	"strings"
	"testing"
)

func TestJSONShape(t *testing.T) {
	s := New([]string{"u1", "u2"})
	s.HTTPReqs.Add(3)
	s.Account("u1").BytesIn.Add(42)
	j := s.JSON()
	for _, k := range []string{"uptime_seconds", "accounts", "session_count", "http_requests", "socks5_conns", "active_conns", "bytes_in", "bytes_out", "rejected", "claims_ok", "claims_failed", "sessions_total", "dial_errors"} {
		if _, ok := j[k]; !ok {
			t.Errorf("JSON missing key %q", k)
		}
	}
	if j["http_requests"] != uint64(3) {
		t.Errorf("http_requests = %v", j["http_requests"])
	}
	if j["accounts"] != 2 {
		t.Errorf("accounts = %v", j["accounts"])
	}
}

type dialErr struct{ msg string }

func (e dialErr) Error() string { return e.msg }

// netErr mimics a net.Error from the standard dialer (e.g. a deadline).
type netErr struct{ dialErr }

func (e netErr) Timeout() bool   { return true }
func (e netErr) Temporary() bool { return true }

func TestDialClassification(t *testing.T) {
	cases := []struct {
		err  error
		want DialReason
	}{
		{dialErr{"dial tcp [2001:db8::1]:443: connect: network is unreachable"}, DialUnreachable},
		{dialErr{"dial tcp [2001:db8::1]:443: connect: no route to host"}, DialUnreachable},
		{dialErr{"dial tcp 1.2.3.4:443: connect: connection refused"}, DialRefused},
		{netErr{dialErr{"i/o timeout"}}, DialTimeout},
		{dialErr{"something else"}, DialOther},
	}
	for _, tc := range cases {
		if got := ClassifyDialErr(tc.err); got != tc.want {
			t.Errorf("ClassifyDialErr(%q) = %s, want %s", tc.err, got, tc.want)
		}
	}
}

func TestWritePrometheusFormat(t *testing.T) {
	s := New([]string{"u1"})
	s.HTTPReqs.Add(1)
	s.ClaimsOK.Add(2)
	s.DialErrors[DialUnreachable].Add(3)

	var b strings.Builder
	s.WritePrometheus(&b, "test", "pool_prefix")
	out := b.String()

	for _, want := range []string{
		"# HELP v6pool_http_requests_total",
		"# TYPE v6pool_http_requests_total counter",
		"v6pool_http_requests_total 1",
		`v6pool_claims_total{result="success"} 2`,
		`v6pool_dial_errors_total{reason="network_unreachable"} 3`,
		`v6pool_info{version="test",pool_mode="pool_prefix"} 1`,
		`v6pool_account_http_requests_total{account="u1"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prometheus output missing %q\n%s", want, out)
		}
	}
	// Every sample line must be either a comment or name[:label] value.
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rest := line
		if i := strings.IndexByte(rest, '{'); i >= 0 {
			if rest[i+1] == '}' {
				rest = rest[i+2:]
			} else {
				if !strings.Contains(rest[i+1:], "} ") {
					t.Errorf("malformed label block in %q", line)
				}
				rest = rest[strings.Index(rest[i+1:], "} ")+i+3:]
			}
		} else if i := strings.IndexAny(rest, " \t"); i >= 0 {
			rest = rest[i:]
		}
		if rest == "" {
			t.Errorf("no value on sample line %q", line)
		}
	}
}
