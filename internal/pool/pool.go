// Package pool generates IPv6 source addresses inside a routed prefix of
// any length (/0-/128).
package pool

import (
	"encoding/binary"
	"net"
)

// gold is the 64-bit golden-ratio constant used to decorrelate splitmix64
// streams (IID vs subnet halves, per-account domains, stream material).
const gold = 0x9E3779B97F4A7C15

// Pool generates IPv6 addresses within a prefix. Host IDs are produced by a
// splitmix64 permutation of a sequence number, so consecutive picks spread
// across the pool instead of walking it linearly.
type Pool struct {
	prefix [16]byte
	bits   int
	seed   uint64
}

// New returns a pool for the given prefix length and prefix address with a
// zero seed (fully deterministic). Bits outside [0,128] is clamped; Config
// validation rejects such values before a Pool is ever built with them.
func New(bits int, prefix net.IP) *Pool {
	return NewWithSeed(bits, prefix, 0)
}

// NewWithSeed returns a pool whose streams are decorrelated by seed: two
// pools with different seeds walk unrelated sequences over the same prefix.
// A random per-boot seed keeps restarts from replaying the same addresses.
func NewWithSeed(bits int, prefix net.IP, seed uint64) *Pool {
	if bits < 0 {
		bits = 0
	}
	if bits > 128 {
		bits = 128
	}
	p := &Pool{bits: bits, seed: seed}
	copy(p.prefix[:], prefix.To16())
	return p
}

// Bits returns the configured prefix length.
func (p *Pool) Bits() int {
	return p.bits
}

// Seed returns the pool's stream seed.
func (p *Pool) Seed() uint64 {
	return p.seed
}

// AccountDomain derives a per-account stream domain from the pool seed so
// different accounts never walk the same address sequence, even with
// identical counters and slices.
func (p *Pool) AccountDomain(idx uint64) uint64 {
	return splitmix64(p.seed ^ (gold * (idx + 1)))
}

// IPFor returns the address for sequence number seq, always inside the
// configured prefix. It is IPForAcct with a zero domain (account 0 with a
// zero seed reproduces the legacy sequence exactly).
//
// The pool's host number is m = 128-bits bits wide. One keyed derivation
// serves every prefix length: sub carries entropy for host bits at/above
// 64, iid for the low 64 host bits. Narrow pools (m <= 64) vary the low m
// bits; wide pools vary subnets plus a fully random IID. Slice ranges
// always carve the pool's coarsest variable field — low host bits for
// narrow pools, subnets (IID fully random) for wide pools.
//
// Hygiene is uniform too: m-bit values 0, 1 and all-ones are skipped
// whenever m > 2, and EUI-64 ff:fe markers are perturbed whenever the
// marker fits fully inside host space (m >= 40).
func (p *Pool) IPFor(seq, start, size uint64) net.IP {
	return p.IPForAcct(seq, 0, start, size)
}

// IPForAcct is IPFor with a per-account domain mixed into the streams.
func (p *Pool) IPForAcct(seq, domain, start, size uint64) net.IP {
	addr := p.prefix
	total := 128 - p.bits
	if total <= 0 {
		return net.IP(addr[:])
	}
	mix := seq ^ p.seed ^ domain
	sub := splitmix64(mix)
	if total <= 64 {
		return ipForNarrow(sub, start, size, total, addr)
	}
	return ipForWide(sub, splitmix64(mix^gold), start, size, p.bits, addr)
}

// ipForNarrow implements IPForAcct for pools whose host number fits 64
// bits: only the low `total` host bits vary, the prefix is preserved.
func ipForNarrow(sub, start, size uint64, total int, addr [16]byte) net.IP {
	prefixLo := binary.BigEndian.Uint64(addr[8:])
	var hostMask, preserveMask uint64
	if total >= 64 {
		hostMask = ^uint64(0)
		preserveMask = 0
	} else {
		hostMask = (uint64(1) << uint(total)) - 1
		preserveMask = ^uint64(0) << uint(total)
	}
	var host uint64
	if size > 0 {
		host = start + sub%size
	} else {
		host = sub & hostMask
	}
	host = sanitizeHost(host, hostMask, total)
	binary.BigEndian.PutUint64(addr[8:], (prefixLo&preserveMask)|host)
	return net.IP(addr[:])
}

// ipForWide implements IPForAcct for pools wider than 64 host bits: the
// subnet field varies in [start,start+size) when sliced and over its full
// width otherwise, while the low-64 IID stays fully random.
func ipForWide(sub, iid, start, size uint64, bits int, addr [16]byte) net.IP {
	highPart := 64 - bits // in (0,64]: wide means bits<64
	var subnetMask = ^uint64(0)
	if highPart < 64 {
		subnetMask = (uint64(1) << uint(highPart)) - 1
	}
	var subnet uint64
	if size > 0 {
		subnet = (start + sub%size) & subnetMask
	} else {
		subnet = sub & subnetMask
	}
	var preserve uint64
	if highPart < 64 {
		preserve = ^uint64(0) << uint(highPart)
	}
	hi := binary.BigEndian.Uint64(addr[:8])
	binary.BigEndian.PutUint64(addr[:8], (hi&preserve)|subnet)
	binary.BigEndian.PutUint64(addr[8:], sanitizeHost(iid, ^uint64(0), 64))
	return net.IP(addr[:])
}

// sanitizeHost maps a raw host value into a host space of `total` bits
// (already masked): values 0, 1 and all-ones are skipped whenever the
// space has more than 2 bits, and EUI-64 ff:fe markers are perturbed
// whenever the marker fits fully inside host space (total >= 40).
// The loop is bounded and deterministic.
func sanitizeHost(v, mask uint64, total int) uint64 {
	v &= mask
	for i := 0; i < 8; i++ {
		switch {
		case total >= 40 && isEUI64(v):
			v ^= 0x0000000200000000 // flip a marker byte bit, stay in space
			v &= mask
		case total > 2 && (v == 0 || v == 1 || v == mask):
			v++
			v &= mask
		default:
			return v
		}
	}
	return v
}

// isEUI64 reports whether the IID carries the EUI-64 ff:fe marker: bytes
// 3-4 of the 8-byte IID (the low word's bits 24-39), the signature of
// MAC-derived SLAAC addresses per RFC 4291 Appendix A.
func isEUI64(iid uint64) bool {
	return (iid>>24)&0xffff == 0xfffe
}

func splitmix64(x uint64) uint64 {
	x += gold
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

// maxCycleSkips bounds hygiene retries inside Stream.Next.
const maxCycleSkips = 8

// Stream is an exact breadth-first cycle over a pool's host space: the
// counter's least-significant bit drives the coarsest host bit, so after
// 2^k picks every group at poolBits+k has appeared exactly once — /49
// halves, then /50 quarters, and so on through every bit level, with zero
// tracking. An odd stride scrambles visit order without disturbing coverage
// (an odd stride permutes residues mod 2^t for every t); a keyed mask
// relocates absolute values. Per-destination streams with distinct tweaks
// walk independent cycles. O(1) state: position, stride, mask.
type Stream struct {
	prefix [16]byte
	bits   int
	width  int // host bits = 128 - bits
	posHi  uint64
	posLo  uint64
	strHi  uint64
	strLo  uint64
	mskHi  uint64
	mskLo  uint64
}

// NewStream returns a cycle for the given account domain and destination
// tweak. All material derives deterministically from the pool seed, so a
// fixed seed replays exactly; proxy feeds a random seed per boot (unless
// pool_seed is pinned) so restarts never replay.
func (p *Pool) NewStream(domain, tweak uint64) *Stream {
	mix := func(salt uint64) uint64 {
		return splitmix64(p.seed ^ domain ^ tweak ^ salt)
	}
	s := &Stream{bits: p.bits, width: 128 - p.bits}
	copy(s.prefix[:], p.prefix[:])
	s.posHi, s.posLo = mix(0x10), mix(0x11)
	s.strHi, s.strLo = mix(0x12), mix(0x13)|1 // odd: full-period cycle
	s.mskHi, s.mskLo = mix(0x14), mix(0x15)
	// Constrain the mask to the m-bit host value (low m bits of the pair).
	switch {
	case s.width >= 128:
		// full pair valid
	case s.width > 64:
		s.mskHi &= (uint64(1) << uint(s.width-64)) - 1
	default:
		s.mskHi = 0
	}
	return s
}

// Next returns the next address in the cycle, advancing past values that
// need hygiene (reserved IDs, EUI-64 markers). Skips consume positions, so
// no address is ever repeated; a bounded retry count guarantees a pick. It
// also returns the number of skipped positions for metrics.
func (s *Stream) Next() (net.IP, uint) {
	ip := s.at(s.posHi, s.posLo)
	var skipped uint
	for i := 0; i < maxCycleSkips && needsHygiene(s.width, ip); i++ {
		s.advance()
		ip = s.at(s.posHi, s.posLo)
		skipped++
	}
	s.advance()
	return ip, skipped
}

// advance moves the position one stride step (128-bit add with carry).
func (s *Stream) advance() {
	lo := s.posLo + s.strLo
	carry := uint64(0)
	if lo < s.posLo {
		carry = 1
	}
	s.posLo = lo
	s.posHi += s.strHi + carry
}

// at maps a counter position to an address: counter bit k drives address
// bit (bits+k), XOR the mask. Counter bit k therefore decides the
// (bits+k) group, giving exact per-bit cascade coverage.
func (s *Stream) at(hi, lo uint64) net.IP {
	addr := s.prefix
	for k := 0; k < s.width; k++ {
		var ebit uint64
		if k < 64 {
			ebit = (lo >> uint(k)) & 1
		} else {
			ebit = (hi >> uint(k-64)) & 1
		}
		var mbit uint64
		j := s.width - 1 - k
		if j < 64 {
			mbit = (s.mskLo >> uint(j)) & 1
		} else {
			mbit = (s.mskHi >> uint(j-64)) & 1
		}
		setAddrBit(addr[:], s.bits+k, ebit^mbit)
	}
	return net.IP(addr[:])
}

// setAddrBit writes bit v at address-bit index b (0 = most significant bit
// of byte 0), clearing whatever the prefix carried there.
func setAddrBit(b []byte, bit int, v uint64) {
	i := bit / 8
	shift := 7 - uint(bit%8)
	if v == 1 {
		b[i] |= 1 << shift
	} else {
		b[i] &^= 1 << shift
	}
}

// needsHygiene reports whether ip should be skipped: reserved m-bit host
// values (0, 1, all-ones) wherever the space has more than 2 host bits,
// and EUI-64 markers wherever the 16-bit marker fits fully inside host
// space (width >= 48). The same host-value rule applies from /0 to /128;
// no prefix length is special-cased.
func needsHygiene(width int, ip net.IP) bool {
	b := ip.To16()
	if b == nil {
		return true
	}
	if width > 2 {
		hi, lo := hostValue(width, b)
		if (hi == 0 && lo == 0) || (hi == 0 && lo == 1) || isAllOnes(width, hi, lo) {
			return true
		}
	}
	if width >= 40 && isEUI64(binary.BigEndian.Uint64(b[8:])) {
		return true
	}
	return false
}

// hostValue extracts the m-bit host number of an address as a (hi, lo)
// pair (hi carries bits above 64).
func hostValue(width int, b net.IP) (hi, lo uint64) {
	lo = binary.BigEndian.Uint64(b[8:])
	if width <= 64 {
		if width < 64 {
			lo &= (uint64(1) << uint(width)) - 1
		}
		return 0, lo
	}
	hi = binary.BigEndian.Uint64(b[:8])
	if width < 128 {
		hi &= (uint64(1) << uint(width-64)) - 1
	}
	return hi, lo
}

// isAllOnes reports whether an m-bit host value has every bit set.
func isAllOnes(width int, hi, lo uint64) bool {
	if lo != ^uint64(0) {
		return false
	}
	if width <= 64 {
		return true
	}
	if width >= 128 {
		return hi == ^uint64(0)
	}
	return hi == (uint64(1)<<uint(width-64))-1
}
