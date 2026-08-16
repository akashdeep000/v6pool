package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// handshakeTimeout bounds the whole SOCKS5 handshake.
const handshakeTimeout = 30 * time.Second

// HandleSOCKS5 serves one SOCKS5 client connection (username/password auth,
// CONNECT only).
func (p *Proxy) HandleSOCKS5(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	p.stats.SOCKSConns.Add(1)
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

	buf := make([]byte, 256)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	if buf[0] != 0x05 {
		return
	}
	nmethods := int(buf[1])
	if nmethods == 0 || nmethods > 128 {
		return
	}
	if _, err := io.ReadFull(conn, buf[:nmethods]); err != nil {
		return
	}
	ok := false
	for _, m := range buf[:nmethods] {
		if m == 0x02 {
			ok = true
		}
	}
	if !ok {
		_, _ = conn.Write([]byte{0x05, 0xFF})
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x02}); err != nil {
		return
	}

	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	ulen := int(buf[1])
	if _, err := io.ReadFull(conn, buf[:ulen]); err != nil {
		return
	}
	user := string(buf[:ulen])
	if _, err := io.ReadFull(conn, buf[:1]); err != nil {
		return
	}
	plen := int(buf[0])
	if _, err := io.ReadFull(conn, buf[:plen]); err != nil {
		return
	}
	pass := string(buf[:plen])

	acct, sessionKey := p.authenticate(user, pass)
	if acct == nil {
		p.stats.Rejected.Add(1)
		_, _ = conn.Write([]byte{0x01, 0x01})
		return
	}
	if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
		return
	}

	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		return
	}
	if buf[0] != 0x05 {
		return
	}
	cmd := buf[1]
	atyp := buf[3]

	var host string
	switch atyp {
	case 0x01:
		if _, err := io.ReadFull(conn, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 0x03:
		if _, err := io.ReadFull(conn, buf[:1]); err != nil {
			return
		}
		l := int(buf[0])
		if _, err := io.ReadFull(conn, buf[:l]); err != nil {
			return
		}
		host = string(buf[:l])
	case 0x04:
		if _, err := io.ReadFull(conn, buf[:16]); err != nil {
			return
		}
		host = net.IP(buf[:16]).String()
	default:
		socksReply(conn, 0x08, nil)
		return
	}
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(buf[:2])

	if cmd != 0x01 {
		socksReply(conn, 0x07, nil)
		return
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	_ = conn.SetDeadline(time.Time{})

	start := time.Now()
	up, err := p.dial(context.Background(), "tcp", addr, acct, sessionKey)
	if err != nil {
		p.logReq(acct, sessionKey, "SOCKS5", addr, 0, nil, time.Since(start))
		socksReply(conn, 0x05, nil)
		return
	}
	defer func() { _ = up.Close() }()

	src, _, _ := net.SplitHostPort(up.LocalAddr().String())
	p.logReq(acct, sessionKey, "SOCKS5", addr, 200, net.ParseIP(src), time.Since(start))
	socksReply(conn, 0x00, net.ParseIP(src))
	p.relay(conn, up)
}

// socksReply writes a SOCKS5 reply, using the IPv6 address type when a v6
// bind address is provided.
func socksReply(conn net.Conn, code byte, bind net.IP) {
	rep := []byte{0x05, code, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	if bind == nil {
		_, _ = conn.Write(rep)
		return
	}
	if v4 := bind.To4(); v4 != nil {
		copy(rep[4:8], v4)
	} else {
		rep[3] = 0x04
		rep = append(rep[:4], bind.To16()...)
		rep = append(rep, 0, 0)
	}
	_, _ = conn.Write(rep)
}
