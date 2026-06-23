// Package metrics defines the core measurement types and statistics used across
// Agent Smith: rolling RTT/loss windows, percentiles, EWMA smoothing, RFC 3550
// interarrival jitter, and the gaming/bufferbloat grading scales described in
// docs/DESIGN.md.
package metrics

import (
	"math"
	"sort"
	"time"
)

// Rating is a qualitative bucket for a measured value, from a gamer's point of view.
type Rating int

const (
	RatingUnknown Rating = iota
	RatingExcellent
	RatingGood
	RatingPlayable
	RatingPoor
)

// String returns a short human label for the rating.
func (r Rating) String() string {
	switch r {
	case RatingExcellent:
		return "Excellent"
	case RatingGood:
		return "Good"
	case RatingPlayable:
		return "Playable"
	case RatingPoor:
		return "Poor"
	default:
		return "Unknown"
	}
}

// Sample is the result of a single probe (one ICMP echo attempt).
type Sample struct {
	When time.Time
	RTT  time.Duration
	OK   bool // false means timeout / no reply (counts as loss)
}

// Stats is a snapshot summary computed over a rolling Window.
type Stats struct {
	Sent   int
	Recv   int
	Loss   float64 // fraction lost in [0,1]
	Min    time.Duration
	Max    time.Duration
	Mean   time.Duration
	P50    time.Duration
	P95    time.Duration
	P99    time.Duration
	StdDev time.Duration
	Jitter time.Duration // RFC 3550 interarrival jitter estimate
	EWMA   time.Duration // exponentially weighted moving average of mean RTT
}

// Window is a fixed-capacity ring buffer of probe samples for one target. It is
// not safe for concurrent use; callers should guard it or use one per goroutine.
type Window struct {
	cap     int
	samples []Sample
	// streaming RFC 3550 jitter state
	jitter   float64 // in seconds
	haveLast bool
	lastRTT  time.Duration
	// EWMA state
	ewma     float64 // in seconds
	haveEWMA bool
	alpha    float64
}

// NewWindow creates a rolling window holding up to capacity samples. The EWMA
// smoothing factor alpha defaults to 0.2 if out of (0,1].
func NewWindow(capacity int, alpha float64) *Window {
	if capacity < 1 {
		capacity = 1
	}
	if alpha <= 0 || alpha > 1 {
		alpha = 0.2
	}
	return &Window{cap: capacity, samples: make([]Sample, 0, capacity), alpha: alpha}
}

// Add records a sample, updating streaming jitter and EWMA. The oldest sample is
// evicted once capacity is exceeded.
func (w *Window) Add(s Sample) {
	if len(w.samples) == w.cap {
		w.samples = w.samples[1:]
	}
	w.samples = append(w.samples, s)

	if s.OK {
		// RFC 3550 interarrival jitter adapted to successive RTT deltas:
		// J += (|D| - J) / 16, where D is the change between consecutive RTTs.
		if w.haveLast {
			d := math.Abs(s.RTT.Seconds() - w.lastRTT.Seconds())
			w.jitter += (d - w.jitter) / 16.0
		}
		w.lastRTT = s.RTT
		w.haveLast = true

		rs := s.RTT.Seconds()
		if !w.haveEWMA {
			w.ewma = rs
			w.haveEWMA = true
		} else {
			w.ewma = w.alpha*rs + (1-w.alpha)*w.ewma
		}
	}
}

// Len returns the number of samples currently held.
func (w *Window) Len() int { return len(w.samples) }

// Stats computes summary statistics over the current window contents.
func (w *Window) Stats() Stats {
	var st Stats
	st.Sent = len(w.samples)
	if st.Sent == 0 {
		return st
	}

	rtts := make([]float64, 0, len(w.samples))
	var sum float64
	for _, s := range w.samples {
		if s.OK {
			rtts = append(rtts, s.RTT.Seconds())
			sum += s.RTT.Seconds()
		}
	}
	st.Recv = len(rtts)
	st.Loss = float64(st.Sent-st.Recv) / float64(st.Sent)
	st.Jitter = secs(w.jitter)
	if w.haveEWMA {
		st.EWMA = secs(w.ewma)
	}
	if st.Recv == 0 {
		return st
	}

	sort.Float64s(rtts)
	mean := sum / float64(st.Recv)

	var sqsum float64
	for _, v := range rtts {
		d := v - mean
		sqsum += d * d
	}
	st.Min = secs(rtts[0])
	st.Max = secs(rtts[len(rtts)-1])
	st.Mean = secs(mean)
	st.StdDev = secs(math.Sqrt(sqsum / float64(st.Recv)))
	st.P50 = secs(percentile(rtts, 50))
	st.P95 = secs(percentile(rtts, 95))
	st.P99 = secs(percentile(rtts, 99))
	return st
}

// percentile returns the p-th percentile of a sorted slice using the
// nearest-rank method. sorted must be non-empty and ascending.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	rank := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func secs(s float64) time.Duration {
	return time.Duration(s * float64(time.Second))
}

// --- Grading scales (see docs/DESIGN.md) ---

// RateLatency buckets an RTT for gaming suitability.
func RateLatency(rtt time.Duration) Rating {
	ms := rtt.Milliseconds()
	switch {
	case rtt <= 0:
		return RatingUnknown
	case ms < 20:
		return RatingExcellent
	case ms < 50:
		return RatingGood
	case ms < 100:
		return RatingPlayable
	default:
		return RatingPoor
	}
}

// RateJitter buckets jitter for gaming suitability.
func RateJitter(j time.Duration) Rating {
	ms := j.Milliseconds()
	switch {
	case j < 0:
		return RatingUnknown
	case ms < 5:
		return RatingExcellent
	case ms < 15:
		return RatingGood
	case ms < 30:
		return RatingPlayable
	default:
		return RatingPoor
	}
}

// RateLoss buckets packet loss fraction (0..1) for gaming suitability.
func RateLoss(loss float64) Rating {
	switch {
	case loss < 0:
		return RatingUnknown
	case loss < 0.001:
		return RatingExcellent
	case loss < 0.01:
		return RatingGood
	case loss < 0.025:
		return RatingPlayable
	default:
		return RatingPoor
	}
}

// BufferbloatGrade maps the latency *increase* under load to the DSLReports
// A+…F scale (added latency above the idle baseline). For gaming, A or B
// (≤ 50 ms added) is the target; C and below indicates a real bufferbloat
// problem fixable with Smart Queue Management on the router.
func BufferbloatGrade(added time.Duration) string {
	ms := added.Milliseconds()
	switch {
	case ms < 0:
		return "A+" // loaded faster than idle (noise) — treat as best
	case ms <= 5:
		return "A+"
	case ms <= 20:
		return "A"
	case ms <= 50:
		return "B"
	case ms <= 100:
		return "C"
	case ms <= 300:
		return "D"
	default:
		return "F"
	}
}

// GradeRating maps a bufferbloat letter grade to a Rating for unified UI coloring.
func GradeRating(grade string) Rating {
	switch grade {
	case "A+", "A":
		return RatingExcellent
	case "B":
		return RatingGood
	case "C":
		return RatingPlayable
	default:
		return RatingPoor
	}
}
