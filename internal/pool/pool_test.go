package pool

import (
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

func TestHostBitsCapsAt64(t *testing.T) {
	p := New(8, net.ParseIP("2001::"))
	if p.hostBits() != 64 {
		t.Errorf("hostBits() = %d, want 64", p.hostBits())
	}
	p = New(128, net.ParseIP("2001:db8::1"))
	if p.hostBits() != 0 {
		t.Errorf("hostBits() = %d, want 0", p.hostBits())
	}
}

func ipLow64(ip net.IP) uint64 {
	b := ip.To16()
	return uint64(b[8])<<56 | uint64(b[9])<<48 | uint64(b[10])<<40 | uint64(b[11])<<32 |
		uint64(b[12])<<24 | uint64(b[13])<<16 | uint64(b[14])<<8 | uint64(b[15])
}
