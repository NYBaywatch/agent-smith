// Package baseline keeps long-term, per-minute aggregates of a metric series
// (RTT for ping targets, total time for HTTP checks) so Agent Smith can answer
// the questions a single rolling window cannot: "what is normal for this
// target?", "is right now abnormal?", and "did we meet our availability /
// latency objective over the last day or week?". It is the single-host
// counterpart to SLA tracking and statistical anomaly detection.
//
// Retention is bounded (default 7 days of one-minute buckets), and everything
// exported is JSON-serialisable so the engine can persist it in the store.
package baseline

import (
	"math"
	"sort"
	"sync"
	"time"
)

// DefaultRetention is how much history a Tracker keeps when none is given.
const DefaultRetention = 7 * 24 * time.Hour

// minBaselineMinutes is how many non-empty minutes a baseline needs to be trusted.
const minBaselineMinutes = 15

// Bucket is one minute of samples for one series.
type Bucket struct {
	T    time.Time `json:"t"`    // truncated to the minute (UTC)
	Sent int       `json:"sent"` // samples attempted
	Recv int       `json:"recv"` // samples that succeeded
	Sum  float64   `json:"sum"`  // ms, over Recv
	Min  float64   `json:"min"`  // ms
	Max  float64   `json:"max"`  // ms
	// P95 is the nearest-rank 95th percentile of this minute's successful
	// samples (ms). It is computed when the bucket is closed; for the open
	// bucket it is computed on demand by Export/Summary.
	P95 float64 `json:"p95"`

	values []float64 // retained only for the currently-open bucket
}

// Mean returns the average of the bucket's successful samples (0 if none).
func (b Bucket) Mean() float64 {
	if b.Recv == 0 {
		return 0
	}
	return b.Sum / float64(b.Recv)
}

// Series is the retained history for one key, ascending by time.
type Series struct {
	Key     string   `json:"key"`
	Buckets []Bucket `json:"buckets"`
}

// Tracker aggregates samples per key into minute buckets. It is safe for
// concurrent use.
type Tracker struct {
	mu        sync.RWMutex
	retention time.Duration
	series    map[string]*Series
}

// NewTracker returns a Tracker retaining the given span of buckets
// (DefaultRetention when retention <= 0).
func NewTracker(retention time.Duration) *Tracker {
	if retention <= 0 {
		retention = DefaultRetention
	}
	return &Tracker{retention: retention, series: make(map[string]*Series)}
}

// Add records one sample for key. ok=false counts toward Sent only (a loss or
// failed check); ms is the measured value when ok.
//
// Samples land in the bucket for their minute. A newer minute closes the open
// bucket (its P95 is finalised and its raw values dropped) and evicts buckets
// older than the retention span. A sample older than the open bucket's minute
// is ignored — history is append-only.
func (t *Tracker) Add(key string, when time.Time, ok bool, ms float64) {
	minute := when.UTC().Truncate(time.Minute)

	t.mu.Lock()
	defer t.mu.Unlock()

	s := t.series[key]
	if s == nil {
		s = &Series{Key: key}
		t.series[key] = s
	}
	var b *Bucket
	if n := len(s.Buckets); n > 0 {
		open := &s.Buckets[n-1]
		switch {
		case minute.Equal(open.T):
			b = open
		case minute.Before(open.T):
			return // older than the open bucket: ignore
		default:
			finalize(open)
			s.Buckets = append(s.Buckets, Bucket{T: minute})
			evict(s, minute.Add(-t.retention))
			b = &s.Buckets[len(s.Buckets)-1]
		}
	} else {
		s.Buckets = append(s.Buckets, Bucket{T: minute})
		b = &s.Buckets[0]
	}

	b.Sent++
	if !ok {
		return
	}
	if b.Recv == 0 || ms < b.Min {
		b.Min = ms
	}
	if ms > b.Max {
		b.Max = ms
	}
	b.Recv++
	b.Sum += ms
	b.values = append(b.values, ms)
}

// finalize computes the closed bucket's P95 and drops its raw values.
func finalize(b *Bucket) {
	b.P95 = p95(b.values)
	b.values = nil
}

// evict drops buckets at or before cutoff, so that a retention of N minutes
// keeps exactly the N most recent minutes.
func evict(s *Series, cutoff time.Time) {
	i := 0
	for i < len(s.Buckets) && !s.Buckets[i].T.After(cutoff) {
		i++
	}
	if i > 0 {
		s.Buckets = append([]Bucket(nil), s.Buckets[i:]...)
	}
}

// p95 returns the nearest-rank 95th percentile of values (0 if empty).
func p95(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(0.95*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// Keys returns the tracked series keys, sorted.
func (t *Tracker) Keys() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	keys := make([]string, 0, len(t.series))
	for k := range t.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Export returns a deep copy of all series (sorted by key) for persistence.
// The open bucket is included with its P95 computed; raw values are not
// exported.
func (t *Tracker) Export() []Series {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Series, 0, len(t.series))
	for _, s := range t.series {
		cp := Series{Key: s.Key, Buckets: make([]Bucket, len(s.Buckets))}
		for i, b := range s.Buckets {
			if b.values != nil {
				b.P95 = p95(b.values)
			}
			b.values = nil
			cp.Buckets[i] = b
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Import replaces the tracker's contents with the given series (tolerant of
// nil). Buckets are copied; the last bucket of each series becomes the open one
// (further samples for its minute are folded in, though its P95 can then only
// reflect values seen after the import).
func (t *Tracker) Import(series []Series) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.series = make(map[string]*Series, len(series))
	for _, s := range series {
		if s.Key == "" {
			continue
		}
		cp := &Series{Key: s.Key, Buckets: make([]Bucket, len(s.Buckets))}
		copy(cp.Buckets, s.Buckets)
		for i := range cp.Buckets {
			cp.Buckets[i].values = nil
		}
		sort.SliceStable(cp.Buckets, func(i, j int) bool { return cp.Buckets[i].T.Before(cp.Buckets[j].T) })
		t.series[s.Key] = cp
	}
}

// Summary aggregates a series over a trailing window.
type Summary struct {
	Window       time.Duration
	Samples      int     // attempted
	Received     int     // succeeded
	Availability float64 // Received/Samples; 1 when there are no samples
	// Mean is the sample-weighted mean (ms). P50 and P95 are approximations:
	// P50 is the Recv-weighted median of the per-minute means, and P95 is the
	// Recv-weighted 95th percentile of the per-minute P95s. Both are exact
	// when every minute holds one sample and become coarser (but still
	// monotone with the underlying data) as minutes hold more samples.
	Mean, P50, P95, Max float64
	Minutes             int // buckets covered
}

// Summary computes the trailing-window aggregate for key ending at now.
func (t *Tracker) Summary(key string, window time.Duration, now time.Time) Summary {
	sum := Summary{Window: window, Availability: 1}
	if window <= 0 {
		return sum
	}
	cutoff := now.UTC().Add(-window).Truncate(time.Minute)

	t.mu.RLock()
	defer t.mu.RUnlock()
	s := t.series[key]
	if s == nil {
		return sum
	}
	type wv struct {
		v float64
		w int
	}
	var means, p95s []wv
	for _, b := range s.Buckets {
		if b.T.Before(cutoff) || b.T.After(now) {
			continue
		}
		sum.Minutes++
		sum.Samples += b.Sent
		sum.Received += b.Recv
		if b.Recv == 0 {
			continue
		}
		sum.Mean += b.Sum
		if b.Max > sum.Max {
			sum.Max = b.Max
		}
		bp := b.P95
		if b.values != nil {
			bp = p95(b.values)
		}
		means = append(means, wv{b.Mean(), b.Recv})
		p95s = append(p95s, wv{bp, b.Recv})
	}
	if sum.Samples > 0 {
		sum.Availability = float64(sum.Received) / float64(sum.Samples)
	}
	if sum.Received == 0 {
		return sum
	}
	sum.Mean /= float64(sum.Received)
	weighted := func(list []wv, p float64) float64 {
		sort.Slice(list, func(i, j int) bool { return list[i].v < list[j].v })
		target := int(math.Ceil(p * float64(sum.Received)))
		acc := 0
		for _, e := range list {
			acc += e.w
			if acc >= target {
				return e.v
			}
		}
		return list[len(list)-1].v
	}
	sum.P50 = weighted(means, 0.5)
	sum.P95 = weighted(p95s, 0.95)
	return sum
}

// Baseline is a robust description of "normal" for a series: the median and
// median absolute deviation of its per-minute means over the last 24 hours.
type Baseline struct {
	Median  float64 // ms
	MAD     float64 // ms
	Minutes int     // non-empty minutes the baseline is built from
	Valid   bool    // enough history to be trusted (>= 15 minutes)
}

// Baseline computes the 24-hour baseline for key as of now.
func (t *Tracker) Baseline(key string, now time.Time) Baseline {
	cutoff := now.UTC().Add(-24 * time.Hour).Truncate(time.Minute)

	t.mu.RLock()
	s := t.series[key]
	var means []float64
	if s != nil {
		for _, b := range s.Buckets {
			if b.Recv == 0 || b.T.Before(cutoff) || b.T.After(now) {
				continue
			}
			means = append(means, b.Mean())
		}
	}
	t.mu.RUnlock()

	bl := Baseline{Minutes: len(means)}
	if len(means) == 0 {
		return bl
	}
	bl.Median = median(means)
	dev := make([]float64, len(means))
	for i, m := range means {
		dev[i] = math.Abs(m - bl.Median)
	}
	bl.MAD = median(dev)
	bl.Valid = bl.Minutes >= minBaselineMinutes
	return bl
}

// median returns the median of values (sorting a copy).
func median(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// Score rates a value against the baseline using a robust z-score,
// z = (value - Median) / (1.4826*MAD + 0.5 ms); the 0.5 ms floor keeps
// ultra-stable links from flagging sub-millisecond wobble. A value is
// anomalous when the baseline is valid, z >= 4, and it sits at least 5 ms above
// the median. An invalid baseline never flags (0, false).
func (b Baseline) Score(valueMs float64) (z float64, anomalous bool) {
	if !b.Valid {
		return 0, false
	}
	z = (valueMs - b.Median) / (1.4826*b.MAD + 0.5)
	return z, z >= 4 && valueMs >= b.Median+5
}

// SLO is a service-level objective for one series.
type SLO struct {
	Availability float64 `json:"availability"` // e.g. 0.999
	P95Ms        float64 `json:"p95_ms"`       // 0 = no latency objective
}

// Compliance is the outcome of checking a Summary against an SLO.
type Compliance struct {
	SLO            SLO
	Summary        Summary
	AvailabilityOK bool
	LatencyOK      bool
	OK             bool
	// ErrorBudgetLeft is the fraction (0..1) of the window's allowed downtime
	// still unspent; negative once the budget is exhausted. With no samples the
	// full budget remains.
	ErrorBudgetLeft float64
}

// Evaluate checks s against slo. Downtime is estimated as
// (1 - Availability) * Window; the budget is (1 - slo.Availability) * Window.
func Evaluate(slo SLO, s Summary) Compliance {
	c := Compliance{SLO: slo, Summary: s, ErrorBudgetLeft: 1}
	c.AvailabilityOK = s.Availability >= slo.Availability
	c.LatencyOK = slo.P95Ms <= 0 || s.P95 <= slo.P95Ms
	c.OK = c.AvailabilityOK && c.LatencyOK
	budget := (1 - slo.Availability) * s.Window.Seconds()
	used := (1 - s.Availability) * s.Window.Seconds()
	switch {
	case budget > 0:
		c.ErrorBudgetLeft = (budget - used) / budget
	case used > 0:
		c.ErrorBudgetLeft = -1 // zero-tolerance objective already breached
	}
	return c
}
