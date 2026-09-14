// Package speedtest measures download and upload throughput from this machine
// while watching latency in both directions. Throughput is the headline most
// people expect from a "speed test"; the part that actually decides whether a
// call or a game feels good is the latency added while the link is busy, so
// every direction also yields a bufferbloat grade on the DSLReports scale.
package speedtest

import (
	"context"
	"fmt"
	"math"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/loadgen"
	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/probe"
)

// Phase names the stage a Progress report belongs to.
type Phase string

const (
	PhaseIdle     Phase = "idle"
	PhaseDownload Phase = "download"
	PhaseUpload   Phase = "upload"
	PhaseDone     Phase = "done"
)

// Progress is a live update emitted during Run.
type Progress struct {
	Phase  Phase
	Pct    float64       // 0..1 overall
	Mbps   float64       // current direction, instantaneous over the last ~1 s
	RTT    time.Duration // latest probe, 0 if lost
	Detail string
}

// Options configures a speed test.
type Options struct {
	Sources         []loadgen.Source // load endpoints, first working one is used
	Connections     int              // parallel streams per direction
	Duration        time.Duration    // measured window per direction (after warm-up)
	WarmUp          time.Duration    // ramp time before measuring
	PingInterval    time.Duration    // latency probe cadence
	PingTarget      net.IP           // latency probe target
	BaselineSamples int              // idle probes before the load
	Progress        func(Progress)   // optional live updates
}

// DefaultOptions returns production defaults: 6 streams, 8 s per direction.
func DefaultOptions() Options {
	return Options{
		Sources:         loadgen.DefaultSources,
		Connections:     6,
		Duration:        8 * time.Second,
		WarmUp:          1500 * time.Millisecond,
		PingInterval:    200 * time.Millisecond,
		PingTarget:      net.IPv4(1, 1, 1, 1),
		BaselineSamples: 10,
	}
}

// Result is the outcome of a speed test.
type Result struct {
	When   time.Time
	Source string // load endpoint used
	Colo   string // CDN point of presence, if known

	IdleRTT, DownRTT, UpRTT time.Duration // medians

	DownMbps, UpMbps         float64 // over the measured window (after warm-up)
	DownPeakMbps, UpPeakMbps float64 // best 1-s interval

	DownAdded, UpAdded time.Duration // latency added under load
	DownGrade, UpGrade string        // DSLReports A+…F ("" when not measured)

	DownBytes, UpBytes uint64
	Duration           time.Duration

	IdleSamples, DownSamples, UpSamples int
}

// minDownloadBytes is the least the download phase must move for the result
// to mean anything.
var minDownloadBytes uint64 = 2 * 1024 * 1024

// Run executes the test: idle baseline, download under latency sampling, a
// short quiet gap, then upload under latency sampling. progress may be nil;
// when both progress and opt.Progress are set, both are called.
func Run(ctx context.Context, p probe.Pinger, opt Options, progress func(Progress)) (Result, error) {
	o := DefaultOptions()
	if len(opt.Sources) > 0 {
		o.Sources = opt.Sources
	}
	if opt.Connections > 0 {
		o.Connections = opt.Connections
	}
	if opt.Duration > 0 {
		o.Duration = opt.Duration
	}
	if opt.WarmUp > 0 {
		o.WarmUp = opt.WarmUp
	}
	if opt.PingInterval > 0 {
		o.PingInterval = opt.PingInterval
	}
	if opt.PingTarget != nil {
		o.PingTarget = opt.PingTarget
	}
	if opt.BaselineSamples > 0 {
		o.BaselineSamples = opt.BaselineSamples
	}
	report := func(pr Progress) {
		if opt.Progress != nil {
			opt.Progress(pr)
		}
		if progress != nil {
			progress(pr)
		}
	}

	res := Result{When: time.Now()}
	start := time.Now()

	// Phase 1: idle baseline.
	var idle []time.Duration
	for i := 0; i < o.BaselineSamples; i++ {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		rtt := ping(ctx, p, o.PingTarget)
		if rtt > 0 {
			idle = append(idle, rtt)
		}
		report(Progress{Phase: PhaseIdle, Pct: 0.1 * float64(i+1) / float64(o.BaselineSamples), RTT: rtt, Detail: "measuring idle latency"})
		if i < o.BaselineSamples-1 {
			sleep(ctx, o.PingInterval)
		}
	}
	res.IdleSamples = len(idle)
	if len(idle) == 0 {
		return res, fmt.Errorf("speedtest: no idle baseline samples (target unreachable?)")
	}
	res.IdleRTT = median(idle)

	// Phase 2: download. A source that answers the probe but throttles the
	// real streams (CDN rate limiting) moves almost nothing; fall back to the
	// next source rather than reporting a meaningless number.
	remaining := append([]loadgen.Source(nil), o.Sources...)
	var src loadgen.Source
	var dl phaseResult
	for {
		var colo string
		var err error
		src, colo, err = loadgen.PickSource(ctx, remaining)
		if err != nil {
			return res, fmt.Errorf("speedtest: %w", err)
		}
		res.Source, res.Colo = src.Name, colo
		dl = runPhase(ctx, p, o, PhaseDownload, 0.1, 0.55, report, func(lctx context.Context, counter *atomic.Uint64) {
			loadgen.Download(lctx, src, o.Connections, counter)
		})
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if dl.bytes >= minDownloadBytes {
			break
		}
		remaining = loadgen.Without(remaining, src.Name)
		if len(remaining) == 0 {
			return res, fmt.Errorf("speedtest: link was not saturated (only %d bytes downloaded from %s); result is not meaningful", dl.bytes, src.Name)
		}
	}
	res.DownBytes, res.DownMbps, res.DownPeakMbps, res.DownSamples = dl.bytes, dl.mbps, dl.peak, len(dl.rtts)
	if len(dl.rtts) > 0 {
		res.DownRTT = median(dl.rtts)
		res.DownAdded = clampAdded(res.DownRTT - res.IdleRTT)
		res.DownGrade = metrics.BufferbloatGrade(res.DownAdded)
	}

	// Quiet gap so the download's queues drain before the upload starts.
	sleep(ctx, time.Second)

	// Phase 3: upload. Use the download source if it accepts uploads,
	// otherwise the first configured source that does (a CDN may throttle
	// large downloads while still accepting uploads).
	upSrc := src
	if upSrc.UpURL == "" {
		for _, cand := range o.Sources {
			if cand.UpURL != "" {
				upSrc = cand
				break
			}
		}
	}
	if upSrc.UpURL != "" {
		ul := runPhase(ctx, p, o, PhaseUpload, 0.55, 1.0, report, func(lctx context.Context, counter *atomic.Uint64) {
			loadgen.Upload(lctx, upSrc, o.Connections, counter)
		})
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		res.UpBytes, res.UpMbps, res.UpPeakMbps, res.UpSamples = ul.bytes, ul.mbps, ul.peak, len(ul.rtts)
		if len(ul.rtts) > 0 && ul.bytes > 0 {
			res.UpRTT = median(ul.rtts)
			res.UpAdded = clampAdded(res.UpRTT - res.IdleRTT)
			res.UpGrade = metrics.BufferbloatGrade(res.UpAdded)
		}
	}

	res.Duration = time.Since(start)
	report(Progress{Phase: PhaseDone, Pct: 1, Mbps: res.DownMbps, RTT: res.IdleRTT, Detail: "done"})
	return res, nil
}

// phaseResult is what one loaded direction measured.
type phaseResult struct {
	bytes uint64
	mbps  float64 // over the measured window
	peak  float64 // best 1-s interval
	rtts  []time.Duration
}

// runPhase drives one direction: start the load, warm up, then sample latency
// for o.Duration while a 1-s ticker computes instantaneous throughput for
// progress reports. pctFrom..pctTo is this phase's slice of overall progress.
func runPhase(ctx context.Context, p probe.Pinger, o Options, phase Phase, pctFrom, pctTo float64, report func(Progress), load func(context.Context, *atomic.Uint64)) phaseResult {
	var pr phaseResult
	loadCtx, cancelLoad := context.WithCancel(ctx)
	defer cancelLoad()

	var counter atomic.Uint64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		load(loadCtx, &counter)
	}()

	total := o.WarmUp + o.Duration
	phaseStart := time.Now()
	pct := func() float64 {
		f := float64(time.Since(phaseStart)) / float64(total)
		if f > 1 {
			f = 1
		}
		return pctFrom + (pctTo-pctFrom)*f
	}

	// Instantaneous throughput for progress (1-s ticker) and the best 1-s
	// interval seen inside the measured window. Both are float bits in
	// atomics so the ticker goroutine and the sampler never race.
	var lastMbps, peakMbps atomic.Uint64
	tickCtx, cancelTick := context.WithCancel(loadCtx)
	var tickWG sync.WaitGroup
	tickWG.Add(1)
	go func() {
		defer tickWG.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		prevBytes, prevT := counter.Load(), time.Now()
		for {
			select {
			case <-tickCtx.Done():
				return
			case now := <-t.C:
				b := counter.Load()
				m := loadgen.Mbps(b-prevBytes, now.Sub(prevT))
				prevBytes, prevT = b, now
				storeFloat(&lastMbps, m)
				if now.After(phaseStart.Add(o.WarmUp)) && m > loadFloat(&peakMbps) {
					storeFloat(&peakMbps, m)
				}
			}
		}
	}()

	// Warm-up: keep the UI alive with progress, no latency samples kept.
	warmDeadline := phaseStart.Add(o.WarmUp)
	for time.Now().Before(warmDeadline) && ctx.Err() == nil {
		rtt := ping(ctx, p, o.PingTarget)
		report(Progress{Phase: phase, Pct: pct(), Mbps: loadFloat(&lastMbps), RTT: rtt, Detail: "ramping up"})
		if time.Now().Add(o.PingInterval).Before(warmDeadline) {
			sleep(ctx, o.PingInterval)
		} else {
			sleep(ctx, warmDeadline.Sub(time.Now()))
		}
	}

	// Measured window.
	startBytes := counter.Load()
	startTime := time.Now()
	deadline := startTime.Add(o.Duration)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		rtt := ping(ctx, p, o.PingTarget)
		if rtt > 0 {
			pr.rtts = append(pr.rtts, rtt)
		}
		report(Progress{Phase: phase, Pct: pct(), Mbps: loadFloat(&lastMbps), RTT: rtt, Detail: "measuring"})
		if time.Now().Add(o.PingInterval).Before(deadline) {
			sleep(ctx, o.PingInterval)
		} else {
			sleep(ctx, deadline.Sub(time.Now()))
		}
	}
	elapsed := time.Since(startTime)
	endBytes := counter.Load()

	cancelTick()
	tickWG.Wait()
	cancelLoad()
	wg.Wait()

	pr.bytes = endBytes - startBytes
	pr.mbps = loadgen.Mbps(pr.bytes, elapsed)
	peak := loadFloat(&peakMbps)
	if peak < pr.mbps {
		peak = pr.mbps
	}
	pr.peak = peak
	return pr
}

func storeFloat(a *atomic.Uint64, f float64) { a.Store(math.Float64bits(f)) }
func loadFloat(a *atomic.Uint64) float64     { return math.Float64frombits(a.Load()) }

// ping sends one probe and returns its RTT, or 0 when lost.
func ping(ctx context.Context, p probe.Pinger, target net.IP) time.Duration {
	pctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	r, err := p.Ping(pctx, target, time.Second)
	if err != nil || !r.OK {
		return 0
	}
	return r.RTT
}

func clampAdded(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), ds...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

// RateThroughput buckets a throughput figure the way the other metrics are
// rated: ≥ 100 Mbps Excellent, ≥ 25 Good, ≥ 5 Fair (Playable), else Poor.
func RateThroughput(mbps float64) metrics.Rating {
	switch {
	case mbps <= 0:
		return metrics.RatingUnknown
	case mbps >= 100:
		return metrics.RatingExcellent
	case mbps >= 25:
		return metrics.RatingGood
	case mbps >= 5:
		return metrics.RatingPlayable
	default:
		return metrics.RatingPoor
	}
}

// Summary turns a Result into two or three plain-language sentences: the
// throughput figures, then what latency did under load and what to do about it.
func Summary(r Result) string {
	up := "upload not measured"
	if r.UpGrade != "" {
		up = fmt.Sprintf("upload %s", fmtMbps(r.UpMbps))
	}
	s := fmt.Sprintf("Download %s, %s.", fmtMbps(r.DownMbps), up)

	downOK := gradeGood(r.DownGrade)
	switch {
	case r.UpGrade == "":
		if downOK {
			s += fmt.Sprintf(" Latency stays flat while downloading (grade %s, +%d ms), so the line copes with a busy download.", r.DownGrade, r.DownAdded.Milliseconds())
		} else {
			s += fmt.Sprintf(" Latency climbs %d ms while downloading (grade %s) — enable SQM/QoS (fq_codel or CAKE) on the router; more bandwidth will not fix it.", r.DownAdded.Milliseconds(), r.DownGrade)
		}
	case downOK && gradeGood(r.UpGrade):
		s += fmt.Sprintf(" Latency stays flat in both directions (download grade %s, +%d ms; upload grade %s, +%d ms), so calls and games should hold up while the link is busy.",
			r.DownGrade, r.DownAdded.Milliseconds(), r.UpGrade, r.UpAdded.Milliseconds())
	case downOK && !gradeGood(r.UpGrade):
		s += fmt.Sprintf(" Latency stays flat while downloading (grade %s, +%d ms) but climbs %d ms while uploading (grade %s) — enable SQM/QoS on the router or cap upload in your apps (cloud backups, streaming).",
			r.DownGrade, r.DownAdded.Milliseconds(), r.UpAdded.Milliseconds(), r.UpGrade)
	case !downOK && gradeGood(r.UpGrade):
		s += fmt.Sprintf(" Latency climbs %d ms while downloading (grade %s) but stays flat while uploading (grade %s, +%d ms) — enable SQM/QoS on the router, or throttle big downloads during calls and games.",
			r.DownAdded.Milliseconds(), r.DownGrade, r.UpGrade, r.UpAdded.Milliseconds())
	default:
		s += fmt.Sprintf(" Latency climbs in both directions (download +%d ms, grade %s; upload +%d ms, grade %s) — classic bufferbloat: enable SQM/QoS (fq_codel or CAKE) on the router; more bandwidth will not fix it.",
			r.DownAdded.Milliseconds(), r.DownGrade, r.UpAdded.Milliseconds(), r.UpGrade)
	}
	return s
}

func gradeGood(g string) bool {
	switch g {
	case "A+", "A", "B":
		return true
	default:
		return false
	}
}

func fmtMbps(m float64) string {
	if m >= 100 {
		return fmt.Sprintf("%.0f Mbps", m)
	}
	return fmt.Sprintf("%.1f Mbps", m)
}
