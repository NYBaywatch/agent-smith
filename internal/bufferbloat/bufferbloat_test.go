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
	if len(o.Sources) < 2 {
		t.Fatalf("expected >=2 fallback load sources, got %d", len(o.Sources))
	}
	if o.Sources[0].UpURL == "" {
		t.Fatal("expected the primary source to support upload")
	}
	if o.Connections < 1 {
		t.Fatal("expected >=1 connection")
	}
}

func TestReportAdaptsProgress(t *testing.T) {
	if (Options{}).report("idle") != nil {
		t.Fatal("no Progress should yield a nil reporter")
	}
	var gotPhase string
	var gotRTT time.Duration
	o := Options{Progress: func(phase string, rtt time.Duration) { gotPhase, gotRTT = phase, rtt }}
	o.report("load")(12 * time.Millisecond)
	if gotPhase != "load" || gotRTT != 12*time.Millisecond {
		t.Fatalf("reporter passed %q/%v", gotPhase, gotRTT)
	}
}
