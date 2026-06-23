package bufferbloat

import (
	"testing"
	"time"
)

func TestMedian(t *testing.T) {
	if got := median(nil); got != 0 {
		t.Fatalf("median(nil) = %v, want 0", got)
	}
	ds := []time.Duration{30, 10, 20, 50, 40}
	for i := range ds {
		ds[i] *= time.Millisecond
	}
	if got := median(ds); got != 30*time.Millisecond {
		t.Fatalf("median = %v, want 30ms", got)
	}
}

func TestDefaultOptionsHasFallbacks(t *testing.T) {
	o := DefaultOptions()
	if len(o.LoadURLs) < 2 {
		t.Fatalf("expected >=2 fallback load URLs, got %d", len(o.LoadURLs))
	}
	if o.Connections < 1 {
		t.Fatal("expected >=1 connection")
	}
}
