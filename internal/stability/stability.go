// Package stability runs a short, dense probe burst — 200 echoes at 50 ms by
// default — to expose what the 1 Hz monitor smooths over: micro-outages, loss
// runs and jitter spikes. It is the "real-time readiness" test: the answer to
// "will this connection hold up for a competitive match or a video call in
// the next ten seconds?".
package stability

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/probe"
)

// Options configures a burst.
type Options struct {
	Target   net.IP
	Name     string        // display name of the target
	Count    int           // probes to send (default 200)
	Interval time.Duration // send cadence, independent of replies (default 50 ms)
	Timeout  time.Duration // per-probe reply timeout (default 1 s)
	// Progress, when set, is called once per probe as its result is known.
	Progress func(done, total int, rtt time.Duration, ok bool)
}

// DefaultOptions returns the standard burst for a target.
func DefaultOptions(target net.IP, name string) Options {
	return Options{Target: target, Name: name, Count: 200, Interval: 50 * time.Millisecond, Timeout: time.Second}
}

// Result is the outcome of a burst.
type Result struct {
	When     time.Time
	Target   string
	Name     string
	Duration time.Duration

	Sent int
	Recv int
	Loss float64 // fraction lost in [0,1]

	Mean, P50, P95, P99, Max, Jitter, StdDev time.Duration

	MaxGap int // longest run of consecutive lost probes
	Spikes int // replies slower than max(2×P50, P50+20 ms)

	Samples []float64 // per-probe RTT in ms, in send order; 0 = lost

	Rating  metrics.Rating
	Verdict string
	Detail  string
}

// Run sends the burst on a fixed schedule — each probe fires on the ticker
// regardless of whether the previous one has answered — and collects replies
// in send order. A cancelled context returns the partial result and ctx.Err().
func Run(ctx context.Context, p probe.Pinger, opt Options) (Result, error) {
	if opt.Count <= 0 {
		opt.Count = 200
	}
	if opt.Interval <= 0 {
		opt.Interval = 50 * time.Millisecond
	}
	if opt.Timeout <= 0 {
		opt.Timeout = time.Second
	}
	res := Result{When: time.Now(), Name: opt.Name}
	if opt.Target != nil {
		res.Target = opt.Target.String()
	}
	if opt.Target == nil {
		return res, fmt.Errorf("stability: no target")
	}

	type outcome struct {
		rtt time.Duration
		ok  bool
	}
	outcomes := make([]outcome, opt.Count)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0

	start := time.Now()
	ticker := time.NewTicker(opt.Interval)
	defer ticker.Stop()

	sent := 0
	var err error
loop:
	for i := 0; i < opt.Count; i++ {
		if i > 0 {
			select {
			case <-ticker.C:
			case <-ctx.Done():
				err = ctx.Err()
				break loop
			}
		} else if ctx.Err() != nil {
			err = ctx.Err()
			break loop
		}
		sent++
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, opt.Timeout)
			r, perr := p.Ping(pctx, opt.Target, opt.Timeout)
			cancel()
			o := outcome{}
			if perr == nil && r.OK {
				o = outcome{rtt: r.RTT, ok: true}
			}
			mu.Lock()
			outcomes[idx] = o
			done++
			d := done
			mu.Unlock()
			if opt.Progress != nil {
				opt.Progress(d, opt.Count, o.rtt, o.ok)
			}
		}(i)
	}
	wg.Wait()
	res.Duration = time.Since(start)

	w := metrics.NewWindow(opt.Count, 0.2)
	res.Samples = make([]float64, sent)
	gap, maxGap := 0, 0
	for i := 0; i < sent; i++ {
		o := outcomes[i]
		w.Add(metrics.Sample{When: start.Add(time.Duration(i) * opt.Interval), RTT: o.rtt, OK: o.ok})
		if o.ok {
			res.Samples[i] = float64(o.rtt) / float64(time.Millisecond)
			gap = 0
			continue
		}
		gap++
		if gap > maxGap {
			maxGap = gap
		}
	}
	st := w.Stats()
	res.Sent, res.Recv, res.Loss = st.Sent, st.Recv, st.Loss
	res.Mean, res.P50, res.P95, res.P99, res.Max = st.Mean, st.P50, st.P95, st.P99, st.Max
	res.Jitter, res.StdDev = st.Jitter, st.StdDev
	res.MaxGap = maxGap
	if st.Recv > 0 {
		thr := 2 * st.P50
		if alt := st.P50 + 20*time.Millisecond; alt > thr {
			thr = alt
		}
		for i := 0; i < sent; i++ {
			if outcomes[i].ok && outcomes[i].rtt > thr {
				res.Spikes++
			}
		}
	}
	res.Rating, res.Verdict, res.Detail = Grade(res)
	return res, err
}

// Grade rates a burst for real-time use and names its worst offender.
func Grade(r Result) (metrics.Rating, string, string) {
	if r.Sent == 0 {
		return metrics.RatingUnknown, "No probes sent", ""
	}
	if r.Recv == 0 {
		return metrics.RatingPoor, "Unreachable — no replies at all", fmt.Sprintf("%d of %d probes lost", r.Sent, r.Sent)
	}
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	var rating metrics.Rating
	var verdict string
	switch {
	case r.Loss < 0.001 && r.Jitter < 5*time.Millisecond && r.P99 < 60*time.Millisecond && r.MaxGap == 0:
		rating, verdict = metrics.RatingExcellent, "Rock solid — ready for competitive play, calls and real-time control"
	case r.Loss < 0.01 && r.Jitter < 15*time.Millisecond && r.P99 < 100*time.Millisecond && r.MaxGap <= 1:
		rating, verdict = metrics.RatingGood, "Steady — fine for gaming, calls and streaming"
	case r.Loss < 0.025 && r.Jitter < 30*time.Millisecond && r.MaxGap <= 3:
		rating, verdict = metrics.RatingPlayable, "Usable, with occasional stutter"
	default:
		rating, verdict = metrics.RatingPoor, "Unstable — expect rubber-banding, frozen frames and retransmits"
	}

	// Name the worst offender: an outage run first, then loss, jitter, spikes.
	var detail string
	switch {
	case r.MaxGap >= 2:
		detail = fmt.Sprintf("%d consecutive probes lost (%.0f ms outage)", r.MaxGap, float64(r.MaxGap)*ms(r.Duration)/float64(max(r.Sent, 1)))
	case r.Loss > 0:
		detail = fmt.Sprintf("%d of %d probes lost (%.1f%%)", r.Sent-r.Recv, r.Sent, r.Loss*100)
	case r.Jitter >= 5*time.Millisecond:
		detail = fmt.Sprintf("jitter %.0f ms", ms(r.Jitter))
	case r.Spikes > 0:
		detail = fmt.Sprintf("%d spikes above %.0f ms", r.Spikes, spikeThresholdMs(r))
	default:
		detail = fmt.Sprintf("p99 %.0f ms, jitter %.1f ms, no loss", ms(r.P99), ms(r.Jitter))
	}
	return rating, verdict, detail
}

func spikeThresholdMs(r Result) float64 {
	thr := 2 * r.P50
	if alt := r.P50 + 20*time.Millisecond; alt > thr {
		thr = alt
	}
	return float64(thr) / float64(time.Millisecond)
}
