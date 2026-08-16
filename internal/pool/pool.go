// Package pool generates IPv6 source addresses inside a routed prefix.
package pool

import (
	"encoding/binary"
	"net"
)

// Pool generates IPv6 addresses within a prefix. Host IDs are produced by a
// splitmix64 permutation of a sequence number, so consecutive picks spread
// across the pool instead of walking it linearly.
type Pool struct {
	prefix [16]byte
	bits   int
}

// New returns a pool for the given prefix length and prefix address.
func New(bits int, prefix net.IP) *Pool {
	p := &Pool{bits: bits}
	copy(p.prefix[:], prefix.To16())
	return p
}

// Bits returns the configured prefix length.
func (p *Pool) Bits() int {
	return p.bits
}

// IPFor returns the address for sequence number seq. When size > 0 the host
// ID is restricted to the sub-pool [start, start+size); otherwise the full
// host range is used. Host IDs 0, 1 (anycast/subnet-router) and the all-ones
// broadcast are always skipped.
func (p *Pool) IPFor(seq, start, size uint64) net.IP {
	host := p.hostBits()
	addr := p.prefix
	salt := splitmix64(seq)
	if size > 0 {
		binary.BigEndian.PutUint64(addr[8:], start+salt%size)
	} else {
		mask := ^uint64(0) >> (64 - host)
		binary.BigEndian.PutUint64(addr[8:], salt&mask)
	}
	lo := binary.BigEndian.Uint64(addr[8:])
	for lo == 0 || lo == 1 || lo == ^uint64(0) {
		lo++
	}
	binary.BigEndian.PutUint64(addr[8:], lo)
	return net.IP(addr[:])
}

// hostBits is the number of bits available for host IDs, capped at 64 (the
// width of the low quad-word used by IPFor).
func (p *Pool) hostBits() int {
	h := 128 - p.bits
	if h < 0 {
		h = 0
	}
	if h > 64 {
		h = 64
	}
	return h
}

func splitmix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}
