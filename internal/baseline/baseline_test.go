package baseline

import (
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

func TestBucketRollover(t *testing.T) {
	tr := NewTracker(0)
	for i := 0; i < 20; i++ {
		tr.Add("a", t0.Add(time.Duration(i)*time.Second), true, float64(10+i))
	}
	tr.Add("a", t0.Add(30*time.Second), false, 0)
	tr.Add("a", t0.Add(time.Minute), true, 50) // new minute closes the first
	tr.Add("a", t0.Add(-time.Hour), true, 999) // older than the open bucket: ignored

	ex := tr.Export()
	if len(ex) != 1 || ex[0].Key != "a" || len(ex[0].Buckets) != 2 {
		t.Fatalf("export = %+v", ex)
	}
	b0, b1 := ex[0].Buckets[0], ex[0].Buckets[1]
	if b0.Sent != 21 || b0.Recv != 20 || b0.Min != 10 || b0.Max != 29 || b0.P95 != 28 {
		t.Errorf("bucket0 = %+v", b0)
	}
	if b1.Sent != 1 || b1.Recv != 1 || b1.P95 != 50 || !b1.T.Equal(t0.Add(time.Minute)) {
		t.Errorf("bucket1 = %+v", b1)
	}
}

func TestEviction(t *testing.T) {
	tr := NewTracker(10 * time.Minute)
	for i := 0; i < 30; i++ {
		tr.Add("a", t0.Add(time.Duration(i)*time.Minute), true, 1)
	}
	ex := tr.Export()
	if n := len(ex[0].Buckets); n != 10 {
		t.Fatalf("retained %d buckets, want 10", n)
	}
	if !ex[0].Buckets[0].T.Equal(t0.Add(20 * time.Minute)) {
		t.Errorf("oldest = %v", ex[0].Buckets[0].T)
	}
}

func TestSummary(t *testing.T) {
	tr := NewTracker(0)
	// 10 minutes, each 6 samples of value 10*(m+1); one loss in minute 3.
	for m := 0; m < 10; m++ {
		for i := 0; i < 6; i++ {
			tr.Add("a", t0.Add(time.Duration(m)*time.Minute+time.Duration(i)*10*time.Second), true, float64(10*(m+1)))
		}
		if m == 3 {
			tr.Add("a", t0.Add(3*time.Minute+55*time.Second), false, 0)
		}
	}
	now := t0.Add(10 * time.Minute)

	s := tr.Summary("a", time.Hour, now)
	if s.Minutes != 10 || s.Samples != 61 || s.Received != 60 {
		t.Fatalf("summary = %+v", s)
	}
	if got, want := s.Availability, 60.0/61.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("availability = %v want %v", got, want)
	}
	if got, want := s.Mean, 55.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("mean = %v want %v", got, want)
	}
	if s.Max != 100 || s.P50 != 50 || s.P95 != 100 {
		t.Errorf("max/p50/p95 = %v/%v/%v", s.Max, s.P50, s.P95)
	}
	if !(s.P50 <= s.P95 && s.P95 <= s.Max) {
		t.Errorf("percentiles not monotone: %+v", s)
	}
	// Narrower window sees fewer minutes.
	s2 := tr.Summary("a", 3*time.Minute, now)
	if s2.Minutes != 3 || s2.Mean != 90 {
		t.Errorf("3-minute summary = %+v", s2)
	}
	// Unknown key / empty.
	if e := tr.Summary("nope", time.Hour, now); e.Availability != 1 || e.Samples != 0 {
		t.Errorf("empty summary = %+v", e)
	}
}

func TestBaselineAndScore(t *testing.T) {
	tr := NewTracker(0)
	// 60 stable minutes alternating 20 / 21 ms.
	for m := 0; m < 60; m++ {
		v := 20.0
		if m%2 == 0 {
			v = 21
		}
		tr.Add("a", t0.Add(time.Duration(m)*time.Minute), true, v)
	}
	now := t0.Add(time.Hour)
	bl := tr.Baseline("a", now)
	if !bl.Valid || bl.Minutes != 60 || math.Abs(bl.Median-20.5) > 1e-9 || math.Abs(bl.MAD-0.5) > 1e-9 {
		t.Fatalf("baseline = %+v", bl)
	}
	if _, an := bl.Score(21); an {
		t.Error("normal value flagged")
	}
	if _, an := bl.Score(24); an { // z high but below the 5 ms absolute floor
		t.Error("value below absolute floor flagged")
	}
	if z, an := bl.Score(100); !an || z < 4 {
		t.Errorf("5x spike not flagged: z=%v anomalous=%v", z, an)
	}

	short := NewTracker(0)
	for m := 0; m < 5; m++ {
		short.Add("a", t0.Add(time.Duration(m)*time.Minute), true, 20)
	}
	sb := short.Baseline("a", now)
	if sb.Valid {
		t.Error("5-minute baseline should be invalid")
	}
	if z, an := sb.Score(1000); z != 0 || an {
		t.Errorf("invalid baseline scored %v/%v", z, an)
	}
}

func TestEvaluate(t *testing.T) {
	slo := SLO{Availability: 0.999, P95Ms: 50}
	// 24h window, one minute lost out of 1440: 99.93 % is still within a
	// 99.9 % objective (the budget is 86.4 s; 60 s used leaves ~0.306).
	s := Summary{Window: 24 * time.Hour, Samples: 1440, Received: 1439, Availability: 1439.0 / 1440.0, P95: 30}
	c := Evaluate(slo, s)
	if !c.AvailabilityOK || !c.OK || !c.LatencyOK {
		t.Errorf("compliance = %+v", c)
	}
	if math.Abs(c.ErrorBudgetLeft-(86.4-60)/86.4) > 1e-6 {
		t.Errorf("budget left = %v", c.ErrorBudgetLeft)
	}
	// Two minutes lost breaches it and overspends the budget.
	s.Received, s.Availability = 1438, 1438.0/1440.0
	if c := Evaluate(slo, s); c.AvailabilityOK || c.OK || c.ErrorBudgetLeft >= 0 {
		t.Errorf("breached compliance = %+v", c)
	}
	// Latency objective missed on its own.
	if c := Evaluate(slo, Summary{Window: time.Hour, Samples: 10, Received: 10, Availability: 1, P95: 80}); c.LatencyOK || c.OK || !c.AvailabilityOK {
		t.Errorf("latency compliance = %+v", c)
	}
	// Fully available, no latency objective.
	c2 := Evaluate(SLO{Availability: 0.999}, Summary{Window: time.Hour, Samples: 10, Received: 10, Availability: 1, P95: 500})
	if !c2.OK || c2.ErrorBudgetLeft != 1 {
		t.Errorf("c2 = %+v", c2)
	}
	// Exhausted budget goes negative.
	c3 := Evaluate(slo, Summary{Window: time.Hour, Samples: 100, Received: 50, Availability: 0.5})
	if c3.ErrorBudgetLeft >= 0 {
		t.Errorf("exhausted budget = %v", c3.ErrorBudgetLeft)
	}
	// No samples: full budget.
	if c4 := Evaluate(slo, Summary{Window: time.Hour, Availability: 1}); c4.ErrorBudgetLeft != 1 || !c4.AvailabilityOK {
		t.Errorf("c4 = %+v", c4)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	tr := NewTracker(0)
	for m := 0; m < 3; m++ {
		for i := 0; i < 4; i++ {
			tr.Add("x", t0.Add(time.Duration(m)*time.Minute+time.Duration(i)*time.Second), i != 3, float64(m*10+i))
		}
	}
	tr.Add("y", t0, true, 7)
	data, err := json.Marshal(tr.Export())
	if err != nil {
		t.Fatal(err)
	}
	var series []Series
	if err := json.Unmarshal(data, &series); err != nil {
		t.Fatal(err)
	}
	tr2 := NewTracker(0)
	tr2.Import(series)
	if got := tr2.Keys(); len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("keys = %v", got)
	}
	da, _ := json.Marshal(tr.Export())
	db, _ := json.Marshal(tr2.Export())
	if string(da) != string(db) {
		t.Errorf("round-trip mismatch:\n%s\n%s", da, db)
	}
	now := t0.Add(3 * time.Minute)
	if s1, s2 := tr.Summary("x", time.Hour, now), tr2.Summary("x", time.Hour, now); s1 != s2 {
		t.Errorf("summary differs after import: %+v vs %+v", s1, s2)
	}
	tr2.Import(nil)
	if len(tr2.Keys()) != 0 {
		t.Error("Import(nil) should clear")
	}
}

func TestConcurrentAdd(t *testing.T) {
	tr := NewTracker(0)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			// All samples fall inside one minute so interleaving never
			// produces an "older than the open bucket" drop.
			for i := 0; i < 200; i++ {
				tr.Add("k", t0.Add(time.Duration(i)*100*time.Millisecond), true, float64(g))
				_ = tr.Summary("k", time.Hour, t0.Add(time.Hour))
				_ = tr.Baseline("k", t0.Add(time.Hour))
			}
		}(g)
	}
	wg.Wait()
	s := tr.Summary("k", time.Hour, t0.Add(time.Hour))
	if s.Samples != 1600 || s.Received != 1600 {
		t.Errorf("summary = %+v", s)
	}
}
