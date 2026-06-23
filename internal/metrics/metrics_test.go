package metrics

import (
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestWindowStatsBasic(t *testing.T) {
	w := NewWindow(10, 0.2)
	for _, v := range []int{10, 20, 30, 40, 50} {
		w.Add(Sample{RTT: ms(v), OK: true})
	}
	st := w.Stats()
	if st.Sent != 5 || st.Recv != 5 {
		t.Fatalf("sent/recv = %d/%d, want 5/5", st.Sent, st.Recv)
	}
	if st.Loss != 0 {
		t.Fatalf("loss = %v, want 0", st.Loss)
	}
	if st.Min != ms(10) || st.Max != ms(50) {
		t.Fatalf("min/max = %v/%v", st.Min, st.Max)
	}
	if st.Mean != ms(30) {
		t.Fatalf("mean = %v, want 30ms", st.Mean)
	}
	// nearest-rank p50 of 5 sorted samples -> index ceil(.5*5)-1 = 2 -> 30ms
	if st.P50 != ms(30) {
		t.Fatalf("p50 = %v, want 30ms", st.P50)
	}
	if st.P99 != ms(50) {
		t.Fatalf("p99 = %v, want 50ms", st.P99)
	}
}

func TestWindowLoss(t *testing.T) {
	w := NewWindow(10, 0.2)
	w.Add(Sample{RTT: ms(10), OK: true})
	w.Add(Sample{OK: false})
	w.Add(Sample{RTT: ms(30), OK: true})
	w.Add(Sample{OK: false})
	st := w.Stats()
	if st.Sent != 4 || st.Recv != 2 {
		t.Fatalf("sent/recv = %d/%d, want 4/2", st.Sent, st.Recv)
	}
	if st.Loss != 0.5 {
		t.Fatalf("loss = %v, want 0.5", st.Loss)
	}
}

func TestWindowCapacityEviction(t *testing.T) {
	w := NewWindow(3, 0.2)
	for _, v := range []int{10, 20, 30, 40, 50} {
		w.Add(Sample{RTT: ms(v), OK: true})
	}
	if w.Len() != 3 {
		t.Fatalf("len = %d, want 3", w.Len())
	}
	st := w.Stats()
	// only last 3 remain: 30,40,50
	if st.Min != ms(30) || st.Max != ms(50) {
		t.Fatalf("after eviction min/max = %v/%v, want 30/50ms", st.Min, st.Max)
	}
}

func TestJitterStreaming(t *testing.T) {
	w := NewWindow(50, 0.2)
	// constant RTT => jitter should converge toward 0
	for i := 0; i < 40; i++ {
		w.Add(Sample{RTT: ms(20), OK: true})
	}
	if j := w.Stats().Jitter; j > ms(1) {
		t.Fatalf("constant RTT jitter = %v, want ~0", j)
	}

	w2 := NewWindow(50, 0.2)
	// alternating RTT => non-zero jitter
	for i := 0; i < 40; i++ {
		v := 10
		if i%2 == 0 {
			v = 60
		}
		w2.Add(Sample{RTT: ms(v), OK: true})
	}
	if j := w2.Stats().Jitter; j <= 0 {
		t.Fatalf("alternating RTT jitter = %v, want > 0", j)
	}
}

func TestRateLatency(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want Rating
	}{
		{ms(10), RatingExcellent},
		{ms(35), RatingGood},
		{ms(80), RatingPlayable},
		{ms(150), RatingPoor},
		{0, RatingUnknown},
	}
	for _, c := range cases {
		if got := RateLatency(c.d); got != c.want {
			t.Errorf("RateLatency(%v) = %v, want %v", c.d, got, c.want)
		}
	}
}

func TestRateLoss(t *testing.T) {
	cases := []struct {
		l    float64
		want Rating
	}{
		{0.0, RatingExcellent},
		{0.005, RatingGood},
		{0.02, RatingPlayable},
		{0.1, RatingPoor},
	}
	for _, c := range cases {
		if got := RateLoss(c.l); got != c.want {
			t.Errorf("RateLoss(%v) = %v, want %v", c.l, got, c.want)
		}
	}
}

func TestBufferbloatGrade(t *testing.T) {
	cases := []struct {
		added time.Duration
		want  string
	}{
		{ms(2), "A+"},
		{ms(15), "A"},
		{ms(45), "B"},
		{ms(90), "C"},
		{ms(250), "D"},
		{ms(600), "F"},
		{ms(-3), "A+"},
	}
	for _, c := range cases {
		if got := BufferbloatGrade(c.added); got != c.want {
			t.Errorf("BufferbloatGrade(%v) = %q, want %q", c.added, got, c.want)
		}
	}
}

func TestPercentileEdges(t *testing.T) {
	one := []float64{0.042}
	if percentile(one, 95) != 0.042 {
		t.Fatal("single-element percentile wrong")
	}
}
