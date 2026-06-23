package sysinfo

import (
	"context"
	"testing"
)

func TestBytesToMbps(t *testing.T) {
	// 1,000,000 bytes in 1 second = 8 Mbps
	if got := bytesToMbps(1_000_000, 1); got != 8 {
		t.Fatalf("bytesToMbps(1e6, 1) = %v, want 8", got)
	}
	// 12,500,000 bytes/s = 100 Mbps
	if got := bytesToMbps(12_500_000, 1); got != 100 {
		t.Fatalf("got %v, want 100", got)
	}
	if got := bytesToMbps(1000, 0); got != 0 {
		t.Fatalf("zero seconds should yield 0, got %v", got)
	}
}

func TestCollectorPrimes(t *testing.T) {
	c := NewCollector()
	// First sample primes throughput and must report 0 Mbps (no prior baseline).
	st, err := c.Sample(context.Background())
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if st.InMbps != 0 || st.OutMbps != 0 {
		t.Fatalf("first sample should report 0 throughput, got in=%v out=%v", st.InMbps, st.OutMbps)
	}
	if !c.primed {
		t.Fatal("collector should be primed after first sample")
	}
}
