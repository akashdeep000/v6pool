package proxy

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// socks5Pair wires a SOCKS5 handler to a client and an upstream through two
// net.Pipe pairs. The test writes requests on client and reads responses from
// it; the handler's upstream connection is visible on upstream.
func socks5Pair(t *testing.T, p *Proxy) (client, upstream net.Conn) {
	t.Helper()
	handlerConn, clientPeer := net.Pipe()
	t.Cleanup(func() { clientPeer.Close() })
	upClient, upPeer := net.Pipe()
	t.Cleanup(func() { upPeer.Close() })
	p.dial = func(ctx context.Context, network, addr string, acct *Account, sessionKey string) (net.Conn, error) {
		return upClient, nil
	}
	go p.HandleSOCKS5(handlerConn)
	return clientPeer, upPeer
}

func socks5Greet(t *testing.T, conn net.Conn) {
	t.Helper()
	conn.Write([]byte{0x05, 0x01, 0x02})
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read method reply: %v", err)
	}
	if buf[0] != 0x05 || buf[1] != 0x02 {
		t.Fatalf("method reply = %v, want {5 2}", buf)
	}
}

func socks5Auth(t *testing.T, conn net.Conn, user, pass string) {
	t.Helper()
	req := []byte{0x01, byte(len(user))}
	req = append(req, []byte(user)...)
	req = append(req, byte(len(pass)))
	req = append(req, []byte(pass)...)
	conn.Write(req)
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read auth reply: %v", err)
	}
	if buf[0] != 0x01 || buf[1] != 0x00 {
		t.Fatalf("auth reply = %v, want success", buf)
	}
}

func socks5Connect(t *testing.T, conn net.Conn, host string, port uint16) {
	t.Helper()
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	req = append(req, byte(port>>8), byte(port&0xff))
	conn.Write(req)
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		t.Fatalf("read connect reply header: %v", err)
	}
	if hdr[0] != 0x05 || hdr[1] != 0x00 {
		t.Fatalf("connect reply code = %d, want 0", hdr[1])
	}
	rest := 6 // IPv4 bind: 4 addr + 2 port
	if hdr[3] == 0x04 {
		rest = 18 // IPv6 bind: 16 addr + 2 port
	}
	if _, err := io.ReadFull(conn, make([]byte, rest)); err != nil {
		t.Fatalf("read connect reply body: %v", err)
	}
}

func TestSOCKS5HandshakeAndRelay(t *testing.T) {
	p := newTestProxy(t)
	client, upstream := socks5Pair(t, p)
	defer upstream.Close()

	socks5Greet(t, client)
	socks5Auth(t, client, "u", "p")
	socks5Connect(t, client, "example.com", 443)

	client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}

	upstream.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(upstream, buf); err != nil {
		t.Fatalf("upstream read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("upstream got %q, want ping", buf)
	}
	if _, err := upstream.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}

	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("client got %q, want pong", buf)
	}
}

func TestSOCKS5RejectsBadPassword(t *testing.T) {
	p := newTestProxy(t)
	client, upstream := socks5Pair(t, p)
	defer upstream.Close()

	socks5Greet(t, client)
	req := []byte{0x01, 0x01, 'u', 0x05}
	req = append(req, []byte("wrong")...)
	client.Write(req)
	buf := make([]byte, 2)
	client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("read auth reply: %v", err)
	}
	if buf[0] != 0x01 || buf[1] != 0x01 {
		t.Fatalf("auth reply = %v, want failure 0x01", buf)
	}
}

func TestSOCKS5ReplyBindsIPv6(t *testing.T) {
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		socksReply(conn, 0x00, net.ParseIP("2001:db8::1"))
	}()
	buf := make([]byte, 22)
	if _, err := io.ReadFull(peer, buf); err != nil {
		t.Fatal(err)
	}
	<-done
	if buf[3] != 0x04 {
		t.Fatalf("atyp = %d, want 0x04 (IPv6)", buf[3])
	}
	if got := net.IP(buf[4:20]).String(); got != "2001:db8::1" {
		t.Fatalf("bound addr = %s", got)
	}
}
