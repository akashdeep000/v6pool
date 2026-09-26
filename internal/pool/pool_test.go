package pool

import (
	"math"
	"net"
	"testing"
)

func TestIPForStaysInPrefix(t *testing.T) {
	p := New(64, net.ParseIP("2001:db8:0:1::"))
	seen := make(map[string]bool)
	for i := uint64(0); i < 1000; i++ {
		ip := p.IPFor(i, 0, 0)
		if !ip.IsGlobalUnicast() {
			t.Fatalf("IPFor(%d) = %s not global unicast", i, ip)
		}
		if !ip.Mask(net.CIDRMask(64, 128)).Equal(net.ParseIP("2001:db8:0:1::")) {
			t.Fatalf("IPFor(%d) = %s outside prefix", i, ip)
		}
		seen[ip.String()] = true
	}
	if len(seen) < 900 {
		t.Errorf("expected well-spread sequence, got %d distinct addresses", len(seen))
	}
}

func TestIPForSkipsReservedHostIDs(t *testing.T) {
	p := New(64, net.ParseIP("2001:db8::"))
	for i := uint64(0); i < 100; i++ {
		lo := ipLow64(p.IPFor(i, 0, 0))
		if lo == 0 || lo == 1 || lo == ^uint64(0) {
			t.Fatalf("IPFor(%d) reserved host id %x", i, lo)
		}
	}
}

func TestIPForAccountSlice(t *testing.T) {
	p := New(64, net.ParseIP("2001:db8::"))
	for i := uint64(0); i < 500; i++ {
		lo := ipLow64(p.IPFor(i, 100, 50))
		if lo < 100 || lo >= 150 {
			t.Fatalf("IPFor(%d) = %x outside account slice [100,150)", i, lo)
		}
	}
}

func TestNewClampsBits(t *testing.T) {
	if got := New(-5, net.ParseIP("2001:db8::")).Bits(); got != 0 {
		t.Errorf("New(-5).Bits() = %d, want 0", got)
	}
	if got := New(200, net.ParseIP("2001:db8::")).Bits(); got != 128 {
		t.Errorf("New(200).Bits() = %d, want 128", got)
	}
}

func TestIPFor128ReturnsPrefix(t *testing.T) {
	want := net.ParseIP("2001:db8::1")
	p := New(128, want)
	for _, seq := range []uint64{0, 1, 42, 1 << 63} {
		if got := p.IPFor(seq, 0, 0); !got.Equal(want) {
			t.Fatalf("IPFor(%d) = %s, want %s", seq, got, want)
		}
	}
}

func TestIPForLongPrefixesStayInPrefix(t *testing.T) {
	for _, bits := range []int{65, 80, 96, 112, 120, 124} {
		prefix := net.ParseIP("2001:db8:1:2:3:4:5:6")
		p := New(bits, prefix)
		mask := net.CIDRMask(bits, 128)
		seen := make(map[string]bool)
		for i := uint64(0); i < 200; i++ {
			ip := p.IPFor(i, 0, 0)
			if !ip.Mask(mask).Equal(prefix.Mask(mask)) {
				t.Fatalf("/%d IPFor(%d) = %s outside prefix", bits, i, ip)
			}
			seen[ip.String()] = true
		}
		if len(seen) < 2 {
			t.Errorf("/%d: expected rotation, got single address", bits)
		}
	}
}

func TestIPFor96PreservesPrefixBytes(t *testing.T) {
	p := New(96, net.ParseIP("2001:db8:1:2:3:4::"))
	for i := uint64(0); i < 100; i++ {
		b := p.IPFor(i, 0, 0).To16()
		// First 12 bytes are the /96 prefix and must never change
		// (pre-generalization code overwrote bytes 8-11 here).
		want := net.ParseIP("2001:db8:1:2:3:4::").To16()[:12]
		for j := 0; j < 12; j++ {
			if b[j] != want[j] {
				t.Fatalf("IPFor(%d) byte %d = %02x, want prefix byte %02x", i, j, b[j], want[j])
			}
		}
	}
}

func TestIPFor0SpreadsFullSpace(t *testing.T) {
	p := New(0, net.ParseIP("::"))
	seen := make(map[string]bool)
	for i := uint64(1); i <= 500; i++ {
		seen[p.IPFor(i, 0, 0).String()] = true
	}
	if len(seen) < 450 {
		t.Errorf("expected spread across full space, got %d distinct", len(seen))
	}
}

func TestIPForLongPrefixAccountSlice(t *testing.T) {
	p := New(120, net.ParseIP("2001:db8::"))
	for i := uint64(0); i < 200; i++ {
		lo := ipLow64(p.IPFor(i, 0xb0, 16))
		if lo < 0xb0 || lo >= 0xc0 {
			t.Fatalf("IPFor(%d) = %x outside slice [b0,c0)", i, lo)
		}
	}
}

func TestIPFor48StaysInPrefix(t *testing.T) {
	p := New(48, net.ParseIP("2a01:d0:b081::"))
	seen := make(map[string]bool)
	for i := uint64(1); i <= 1000; i++ {
		ip := p.IPFor(i, 0, 0)
		if !ip.IsGlobalUnicast() {
			t.Fatalf("IPFor(%d) = %s not global unicast", i, ip)
		}
		if !ip.Mask(net.CIDRMask(48, 128)).Equal(net.ParseIP("2a01:d0:b081::")) {
			t.Fatalf("IPFor(%d) = %s outside /48", i, ip)
		}
		seen[ip.String()] = true
	}
	if len(seen) < 900 {
		t.Errorf("expected well-spread sequence, got %d distinct addresses", len(seen))
	}
}

func TestIPFor48SpreadsAcrossSubnets(t *testing.T) {
	p := New(48, net.ParseIP("2a01:d0:b081::"))
	subnets := make(map[string]bool)
	for i := uint64(1); i <= 200; i++ {
		subnets[subnet64(p.IPFor(i, 0, 0))] = true
	}
	// 200 sequential picks must cover many distinct /64s, not collapse
	// into the first /64 (the pre-/48-cap behavior).
	if len(subnets) < 100 {
		t.Errorf("expected spread across /64s, got %d distinct subnets", len(subnets))
	}
}

func TestIPFor48AccountSubnetSlice(t *testing.T) {
	p := New(48, net.ParseIP("2a01:d0:b081::"))
	for i := uint64(1); i <= 200; i++ {
		ip := p.IPFor(i, 10, 5)
		sub := ipSubnetID(ip)
		if sub < 10 || sub >= 15 {
			t.Fatalf("IPFor(%d) = %s subnet %d outside slice [10,15)", i, ip, sub)
		}
	}
}

func TestIPFor48SkipsReservedIIDs(t *testing.T) {
	p := New(48, net.ParseIP("2a01:d0:b081::"))
	for i := uint64(0); i < 200; i++ {
		lo := ipLow64(p.IPFor(i, 0, 0))
		if lo == 0 || lo == 1 || lo == ^uint64(0) {
			t.Fatalf("IPFor(%d) reserved IID %x", i, lo)
		}
	}
}

func ipLow64(ip net.IP) uint64 {
	b := ip.To16()
	return uint64(b[8])<<56 | uint64(b[9])<<48 | uint64(b[10])<<40 | uint64(b[11])<<32 |
		uint64(b[12])<<24 | uint64(b[13])<<16 | uint64(b[14])<<8 | uint64(b[15])
}

func subnet64(ip net.IP) string {
	b := ip.To16()
	return string(b[:8])
}

func ipSubnetID(ip net.IP) uint64 {
	b := ip.To16()
	return uint64(b[6])<<8 | uint64(b[7])
}

// firstBits returns the top k bits of ip as a string key (test helper).
func firstBits(ip net.IP, k int) string {
	b := ip.To16()
	out := make([]byte, 0, k/8+1)
	out = append(out, b[:k/8]...)
	if k%8 != 0 {
		out = append(out, b[k/8]&(^uint8(0)<<uint(8-k%8)))
	}
	return string(out)
}

func TestStreamCycleExact60(t *testing.T) {
	p := NewWithSeed(60, net.ParseIP("2a01:d0:b081::"), 0x1234)
	st := p.NewStream(p.AccountDomain(0), 0xabcd)
	// A /60 holds 16 /64s: the first 16 picks cover all of them exactly
	// once (breadth-first cascade, subnet path needs no hygiene).
	seen := make(map[string]bool)
	for i := 0; i < 16; i++ {
		ip, _ := st.Next()
		k := firstBits(ip, 64)
		if seen[k] {
			t.Fatalf("pick %d reused /64 %s before exhaustion", i+1, ip)
		}
		seen[k] = true
		if !ip.Mask(net.CIDRMask(60, 128)).Equal(net.ParseIP("2a01:d0:b081::")) {
			t.Fatalf("pick %d = %s outside /60", i+1, ip)
		}
	}
}

func TestStreamCascadeOneBitPerDoubling(t *testing.T) {
	p := NewWithSeed(60, net.ParseIP("2a01:d0:b081::"), 0x1234)
	st := p.NewStream(0, 0)
	// After 2 picks both /61 halves; after 4 all /62 quarters (exact,
	// deterministic: subnet path is hygiene-free).
	halves := map[string]bool{}
	quarters := map[string]bool{}
	for i := 0; i < 4; i++ {
		ip, _ := st.Next()
		halves[firstBits(ip, 61)] = true
		quarters[firstBits(ip, 62)] = true
	}
	if len(halves) != 2 {
		t.Errorf("2 picks cover %d /61 halves, want 2", len(halves))
	}
	if len(quarters) != 4 {
		t.Errorf("4 picks cover %d /62 quarters, want 4", len(quarters))
	}
}

func TestStreamWrapAdvancesFinerLevel(t *testing.T) {
	p := NewWithSeed(60, net.ParseIP("2a01:d0:b081::"), 0x1234)
	st := p.NewStream(0, 0)
	var first net.IP
	for i := 0; i < 17; i++ {
		ip, _ := st.Next()
		if i == 0 {
			first = ip
		}
		if i == 16 {
			// 17th pick re-enters the 1st /64 but in the opposite /65.
			if firstBits(ip, 64) != firstBits(first, 64) {
				t.Fatalf("pick 17 = %s left /64 %s", ip, first)
			}
			if firstBits(ip, 65) == firstBits(first, 65) {
				t.Fatalf("pick 17 = %s did not advance /65 within %s", ip, first)
			}
		}
	}
}

func TestStreamTweakIndependence(t *testing.T) {
	p := NewWithSeed(48, net.ParseIP("2a01:d0:b081::"), 9)
	a, b := p.NewStream(0, 0xaa), p.NewStream(0, 0xbb)
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		ip, _ := a.Next()
		seen[ip.String()] = true
	}
	for i := 0; i < 50; i++ {
		ip, _ := b.Next()
		if seen[ip.String()] {
			t.Fatalf("tweaked streams share address %s", ip)
		}
	}
}

func TestStreamNoEUI64(t *testing.T) {
	p := NewWithSeed(48, net.ParseIP("2a01:d0:b081::"), 0x55)
	st := p.NewStream(0, 0)
	for i := 0; i < 20000; i++ {
		ip, _ := st.Next()
		if isEUI64(ipLow64(ip)) {
			t.Fatalf("pick %d carries an EUI-64 marker: %s", i+1, ip)
		}
		if !ip.IsGlobalUnicast() {
			t.Fatalf("pick %d = %s not global unicast", i+1, ip)
		}
	}
}

func TestNoEUI64Markers(t *testing.T) {
	for _, bits := range []int{48, 56, 64} {
		p := New(bits, net.ParseIP("2a01:d0:b081::"))
		for i := uint64(1); i <= 5000; i++ {
			if isEUI64(ipLow64(p.IPFor(i, 0, 0))) {
				t.Fatalf("/%d IPFor(%d) carries an EUI-64 ff:fe marker", bits, i)
			}
		}
	}
}

func TestAccountDomainsDiffer(t *testing.T) {
	p := NewWithSeed(48, net.ParseIP("2a01:d0:b081::"), 7)
	d0, d1 := p.AccountDomain(0), p.AccountDomain(1)
	if d0 == d1 {
		t.Fatal("account domains collide")
	}
	seen := make(map[string]bool)
	for i := uint64(1); i <= 200; i++ {
		seen[p.IPForAcct(i, d0, 0, 0).String()] = true
	}
	for i := uint64(1); i <= 200; i++ {
		if seen[p.IPForAcct(i, d1, 0, 0).String()] {
			t.Fatalf("accounts share address %s", p.IPForAcct(i, d1, 0, 0))
		}
	}
}

func TestSeedChangesSequence(t *testing.T) {
	a := NewWithSeed(48, net.ParseIP("2a01:d0:b081::"), 1)
	b := NewWithSeed(48, net.ParseIP("2a01:d0:b081::"), 2)
	same := 0
	for i := uint64(1); i <= 10; i++ {
		if a.IPFor(i, 0, 0).Equal(b.IPFor(i, 0, 0)) {
			same++
		}
	}
	if same == 10 {
		t.Fatal("different seeds produced identical sequences")
	}
	// Zero seed reproduces the legacy unseeded sequence.
	legacy, seeded := New(48, net.ParseIP("2a01:d0:b081::")), NewWithSeed(48, net.ParseIP("2a01:d0:b081::"), 0)
	for i := uint64(1); i <= 20; i++ {
		if !legacy.IPFor(i, 0, 0).Equal(seeded.IPFor(i, 0, 0)) {
			t.Fatalf("zero seed diverged from legacy at %d", i)
		}
	}
}

func TestIPForUniformSweep(t *testing.T) {
	// Same contract at every prefix length, octet or not: containment,
	// spread, and reserved hygiene. A /64-shaped special case would show
	// up here as a discontinuity across the 64 boundary.
	for _, bits := range []int{33, 40, 47, 48, 55, 59, 61, 63, 64, 65, 72, 80, 88, 89, 95, 100, 112, 120} {
		p := NewWithSeed(bits, net.ParseIP("2a01:d0:b081:1234:5678:9abc:def0:1234"), 0x77)
		mask := net.CIDRMask(bits, 128)
		want := net.ParseIP("2a01:d0:b081:1234:5678:9abc:def0:1234").Mask(mask)
		seen := make(map[string]bool)
		for i := uint64(1); i <= 500; i++ {
			ip := p.IPFor(i, 0, 0)
			if !ip.Mask(mask).Equal(want) {
				t.Fatalf("/%d IPFor(%d) = %s outside prefix", bits, i, ip)
			}
			seen[ip.String()] = true
		}
		// Expect near-full coverage of small spaces, wide spread otherwise.
		// Uniform sampling over `card` values yields card*(1-(1-1/card)^500).
		total := 128 - bits
		wantDistinct := 400
		if total < 30 {
			card := float64(uint64(1) << uint(total))
			exp := card * (1 - math.Pow(1-1/card, 500))
			wantDistinct = int(exp * 0.9)
		}
		if len(seen) < wantDistinct {
			t.Errorf("/%d: only %d distinct over 500 picks, want >= %d", bits, len(seen), wantDistinct)
		}
	}
}

func TestIsEUI64TruePosition(t *testing.T) {
	// MAC 00:11:22:33:44:55 -> IID 0211:22ff:fe33:4455 per RFC 4291 A:
	// the marker straddles bytes 11-12, not hextet 5.
	if !isEUI64(0x021122fffe334455) {
		t.Error("real MAC-derived IID not detected")
	}
	if isEUI64(0x0211223303344455) {
		t.Error("non-EUI IID falsely detected")
	}
	// Whole-hextet fffe at bytes 10-11 is not an EUI marker.
	if isEUI64(0x00000000fffe0000) {
		t.Error("hextet-aligned fffe falsely detected as EUI")
	}
}

func TestNeedsHygieneUniform(t *testing.T) {
	cases := []struct {
		name string
		bits int
		ip   string
		want bool
	}{
		{"48 all-zero host", 48, "2a01:d0:b081::", true},
		{"48 host one", 48, "2a01:d0:b081::1", true},
		{"48 all-ones host", 48, "2a01:d0:b081:ffff:ffff:ffff:ffff:ffff", true},
		{"48 ordinary", 48, "2a01:d0:b081:1::5", false},
		{"48 eui marker", 48, "2a01:d0:b081::11ff:fe22:0", true},
		{"64 eui marker", 64, "2001:db8::11ff:fe22:0", true},
		{"64 ordinary", 64, "2001:db8::5", false},
		{"96 zero host", 96, "2001:db8:1:2:3:4::", true},
		{"96 ordinary", 96, "2001:db8:1:2:3:4::5", false},
		// ff:fe embedded in fixed prefix bits must not skip: the marker
		// is only meaningful inside host space.
		{"96 prefix marker, ordinary host", 96, "2001:db8::fffe:0:0:5", false},
		{"120 zero host", 120, "2001:db8::1:0", true},
		{"127 either address", 127, "2001:db8::1", false},
		{"128 single address", 128, "2001:db8::1", false},
	}
	for _, tc := range cases {
		if got := needsHygiene(128-tc.bits, net.ParseIP(tc.ip)); got != tc.want {
			t.Errorf("%s: needsHygiene = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSanitizeHostUniform(t *testing.T) {
	// Reserved values collapse identically at widths on both sides of 64.
	for _, total := range []int{3, 47, 48, 63, 64, 65} {
		mask := ^uint64(0)
		if total < 64 {
			mask = (uint64(1) << uint(total)) - 1
		}
		if got := sanitizeHost(0, mask, total); got == 0 || got == 1 || got == mask {
			t.Errorf("total=%d: reserved 0 survived as %x", total, got)
		}
		if got := sanitizeHost(mask, mask, total); got == 0 || got == 1 || got == mask {
			t.Errorf("total=%d: reserved all-ones survived as %x", total, got)
		}
	}
	// Tiny spaces (<=2 bits) cannot avoid reserved values: return as-is.
	if got := sanitizeHost(0, 0x3, 2); got != 0 {
		t.Errorf("total=2: got %x, want passthrough 0", got)
	}
}
