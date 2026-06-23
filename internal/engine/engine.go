// Package engine orchestrates Agent Smith's probes and collectors on a schedule
// and produces a stream of model.Snapshot values for the UIs. It pings the
// concentric rings (gateway → ISP edge → public anchors), refreshes local
// topology and DNS latency periodically, samples host resources every tick, and
// runs the classifier to attach a verdict to each snapshot.
package engine

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/bufferbloat"
	"github.com/NYBaywatch/agent-smith/internal/classifier"
	"github.com/NYBaywatch/agent-smith/internal/dnsprobe"
	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/model"
	"github.com/NYBaywatch/agent-smith/internal/netinfo"
	"github.com/NYBaywatch/agent-smith/internal/probe"
	"github.com/NYBaywatch/agent-smith/internal/store"
	"github.com/NYBaywatch/agent-smith/internal/sysinfo"
)

// Retention caps for persisted state.
const (
	histCap  = 3600 // ~1 hour of RTT history at 1/s
	issueCap = 200  // most recent recorded issues
)

// Target is a host to probe and the path role it represents.
type Target struct {
	Name string
	Host string
	Role model.Role
}

// Config tunes the engine's cadences and probe set.
type Config struct {
	Interval         time.Duration // per-target probe cadence
	Window           int           // rolling samples retained per target
	Timeout          time.Duration // per-probe timeout
	Anchors          []Target      // public internet anchors (and optional game servers)
	DNSInterval      time.Duration // how often to measure DNS latency
	TopologyInterval time.Duration // how often to re-discover gateway/ISP hop
	MaxTraceHops     int           // cap for ISP-hop discovery
}

// DefaultConfig returns production-sensible defaults.
func DefaultConfig() Config {
	return Config{
		Interval: time.Second,
		Window:   30,
		Timeout:  time.Second,
		Anchors: []Target{
			{Name: "Cloudflare", Host: "1.1.1.1", Role: model.RoleInternet},
			{Name: "Google", Host: "8.8.8.8", Role: model.RoleInternet},
		},
		DNSInterval:      15 * time.Second,
		TopologyInterval: 30 * time.Second,
		MaxTraceHops:     8,
	}
}

// Engine runs the monitoring loops and holds the latest snapshot.
type Engine struct {
	cfg    Config
	pinger probe.Pinger
	sys    *sysinfo.Collector

	mu      sync.RWMutex
	windows map[string]*metrics.Window // keyed by host
	gateway *Target
	ispHop  *Target
	netInfo *netinfo.Info
	dns     dnsprobe.Result
	lastBB  *bufferbloat.Result
	latest  model.Snapshot
	subs    []chan model.Snapshot

	// session history + issue log (persisted)
	history      []model.HistPoint
	issues       []model.Issue
	lastIssueKey string
	lastIssueAt  time.Time
}

// New constructs an Engine with the given config.
func New(cfg Config) (*Engine, error) {
	p, err := probe.New()
	if err != nil {
		return nil, err
	}
	e := &Engine{
		cfg:     cfg,
		pinger:  p,
		sys:     sysinfo.NewCollector(),
		windows: make(map[string]*metrics.Window),
	}
	// Restore persisted history + issues (best effort).
	if st, err := store.Load(); err == nil {
		e.history = st.History
		e.issues = st.Issues
	}
	return e, nil
}

// Pinger exposes the underlying pinger (used for on-demand bufferbloat tests).
func (e *Engine) Pinger() probe.Pinger { return e.pinger }

// Subscribe returns a channel that receives every new snapshot. The channel is
// buffered and lossy: if a consumer is slow, snapshots are dropped rather than
// blocking the engine.
func (e *Engine) Subscribe() <-chan model.Snapshot {
	ch := make(chan model.Snapshot, 4)
	e.mu.Lock()
	e.subs = append(e.subs, ch)
	e.mu.Unlock()
	return ch
}

// Latest returns the most recent snapshot.
func (e *Engine) Latest() model.Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.latest
}

// Run starts the engine loops and blocks until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	defer e.pinger.Close()

	e.refreshTopology(ctx)
	e.refreshDNS(ctx)

	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); e.probeLoop(ctx) }()
	go func() { defer wg.Done(); e.periodic(ctx, e.cfg.TopologyInterval, e.refreshTopology) }()
	go func() { defer wg.Done(); e.periodic(ctx, e.cfg.DNSInterval, e.refreshDNS) }()
	go func() { defer wg.Done(); e.periodic(ctx, 30*time.Second, func(context.Context) { e.save() }) }()

	<-ctx.Done()
	wg.Wait()
	e.save() // flush on shutdown
	return ctx.Err()
}

func (e *Engine) periodic(ctx context.Context, every time.Duration, fn func(context.Context)) {
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

func (e *Engine) probeLoop(ctx context.Context) {
	t := time.NewTicker(e.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tick(ctx)
		}
	}
}

// tick probes all current targets concurrently and publishes a snapshot.
func (e *Engine) tick(ctx context.Context) {
	targets := e.currentTargets()

	var wg sync.WaitGroup
	for _, tg := range targets {
		wg.Add(1)
		go func(tg Target) {
			defer wg.Done()
			ip := net.ParseIP(tg.Host)
			if ip == nil {
				return
			}
			pctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
			r, err := e.pinger.Ping(pctx, ip, e.cfg.Timeout)
			cancel()
			s := metrics.Sample{When: time.Now()}
			if err == nil && r.OK {
				s.RTT, s.OK = r.RTT, true
			}
			e.addSample(tg.Host, s)
		}(tg)
	}
	wg.Wait()

	sys, _ := e.sys.Sample(ctx)
	snap := e.buildSnapshot(sys)
	snap.Verdict = classifier.Classify(snap)
	e.publish(snap)
	e.recordHistory(snap)
	e.maybeRecordIssue(ctx, snap)
}

// recordHistory appends a sampled RTT point for the sparkline / persistence.
func (e *Engine) recordHistory(snap model.Snapshot) {
	hp := model.HistPoint{
		T:     snap.Time,
		GwMs:  rttMs(snap.Gateway),
		IspMs: rttMs(snap.ISPHop),
		NetMs: bestInternetMs(snap),
	}
	e.mu.Lock()
	e.history = append(e.history, hp)
	if len(e.history) > histCap {
		e.history = e.history[len(e.history)-histCap:]
	}
	e.mu.Unlock()
}

// maybeRecordIssue logs a new issue when the verdict transitions into (or between)
// unhealthy states, capturing a process snapshot asynchronously so the probe loop
// is never blocked by process enumeration.
func (e *Engine) maybeRecordIssue(ctx context.Context, snap model.Snapshot) {
	v := snap.Verdict
	unhealthy := v.Severity >= model.SevDegraded
	key := fmt.Sprintf("%d/%d", v.Severity, v.Culprit)

	e.mu.Lock()
	prev := e.lastIssueKey
	if unhealthy {
		e.lastIssueKey = key
	} else {
		e.lastIssueKey = "ok"
	}
	since := snap.Time.Sub(e.lastIssueAt)
	shouldLog := unhealthy && key != prev && since >= 8*time.Second
	if shouldLog {
		e.lastIssueAt = snap.Time
	}
	e.mu.Unlock()

	if !shouldLog {
		return
	}
	at := snap.Time
	mx := issueMetrics(snap)
	go func() {
		var procs []model.ProcInfo
		if top, err := sysinfo.TopCPUProcesses(ctx, 6); err == nil {
			for _, p := range top {
				procs = append(procs, model.ProcInfo{PID: p.PID, Name: p.Name, CPU: p.CPU, MemMB: p.MemMB})
			}
		}
		iss := model.Issue{
			Time: at, Severity: v.Severity, Culprit: v.Culprit,
			Headline: v.Headline, Detail: v.Detail, Fix: v.Fix, Metrics: mx, Procs: procs,
		}
		e.mu.Lock()
		e.issues = append(e.issues, iss)
		if len(e.issues) > issueCap {
			e.issues = e.issues[len(e.issues)-issueCap:]
		}
		e.mu.Unlock()
		e.save()
	}()
}

// History returns a copy of the recorded RTT history.
func (e *Engine) History() []model.HistPoint {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]model.HistPoint, len(e.history))
	copy(out, e.history)
	return out
}

// Issues returns a copy of the recorded issue log (oldest first).
func (e *Engine) Issues() []model.Issue {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]model.Issue, len(e.issues))
	copy(out, e.issues)
	return out
}

// ClearIssues empties the recorded issue log and persists the change.
func (e *Engine) ClearIssues() {
	e.mu.Lock()
	e.issues = nil
	e.lastIssueKey = ""
	e.mu.Unlock()
	e.save()
}

// save persists current history + issues (best effort).
func (e *Engine) save() {
	e.mu.RLock()
	st := store.State{
		History: append([]model.HistPoint(nil), e.history...),
		Issues:  append([]model.Issue(nil), e.issues...),
	}
	e.mu.RUnlock()
	_ = store.Save(st)
}

func rttMs(ts *model.TargetStats) float64 {
	if ts == nil || !ts.Alive {
		return 0
	}
	return float64(ts.Stats.Mean) / float64(time.Millisecond)
}

func bestInternetMs(snap model.Snapshot) float64 {
	if ts := bestInternetTS(snap); ts != nil {
		return float64(ts.Stats.Mean) / float64(time.Millisecond)
	}
	return 0
}

// bestInternetTS returns the healthiest alive anchor (lowest mean RTT), or nil.
func bestInternetTS(snap model.Snapshot) *model.TargetStats {
	var best *model.TargetStats
	for i := range snap.Internet {
		ts := &snap.Internet[i]
		if !ts.Alive {
			continue
		}
		if best == nil || ts.Stats.Mean < best.Stats.Mean {
			best = ts
		}
	}
	return best
}

// issueMetrics extracts the concrete measured values behind a verdict.
func issueMetrics(snap model.Snapshot) model.IssueMetrics {
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	mx := model.IssueMetrics{
		GatewayMs:   rttMs(snap.Gateway),
		ISPMs:       rttMs(snap.ISPHop),
		InternetMs:  bestInternetMs(snap),
		CPUPct:      snap.Sys.CPUPercent,
		MemPct:      snap.Sys.MemPercent,
		MemUsedGB:   snap.Sys.MemUsedGB,
		MemTotalGB:  snap.Sys.MemTotalGB,
		GPUPct:      snap.Sys.GPUPercent,
		DNSms:       ms(snap.DNS.Avg),
		Bufferbloat: bufferbloatGrade(snap),
	}
	if b := bestInternetTS(snap); b != nil {
		mx.InternetJitterMs = ms(b.Stats.Jitter)
		mx.InternetLossPct = b.Stats.Loss * 100
	}
	if snap.Net != nil && snap.Net.WiFi != nil {
		mx.OnWiFi = true
		mx.RSSI = snap.Net.WiFi.RSSI
	}
	return mx
}

func bufferbloatGrade(snap model.Snapshot) string {
	if snap.Bufferbloat != nil {
		return snap.Bufferbloat.Grade
	}
	return ""
}

func (e *Engine) currentTargets() []Target {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []Target
	if e.gateway != nil {
		out = append(out, *e.gateway)
	}
	if e.ispHop != nil {
		out = append(out, *e.ispHop)
	}
	out = append(out, e.cfg.Anchors...)
	return out
}

func (e *Engine) addSample(host string, s metrics.Sample) {
	e.mu.Lock()
	defer e.mu.Unlock()
	w := e.windows[host]
	if w == nil {
		w = metrics.NewWindow(e.cfg.Window, 0.3)
		e.windows[host] = w
	}
	w.Add(s)
}

func (e *Engine) statsFor(host string) (metrics.Stats, bool) {
	w := e.windows[host]
	if w == nil {
		return metrics.Stats{}, false
	}
	st := w.Stats()
	return st, st.Recv > 0
}

// buildSnapshot assembles a snapshot from current window state. Caller passes a
// fresh sysinfo sample.
func (e *Engine) buildSnapshot(sys sysinfo.Stats) model.Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()

	snap := model.Snapshot{
		Time:        time.Now(),
		Net:         e.netInfo,
		Sys:         sys,
		DNS:         e.dns,
		Bufferbloat: e.lastBB,
	}

	if e.gateway != nil {
		st, alive := e.statsFor(e.gateway.Host)
		snap.Gateway = &model.TargetStats{Name: e.gateway.Name, Host: e.gateway.Host, Role: model.RoleGateway, Stats: st, Alive: alive}
	}
	if e.ispHop != nil {
		st, alive := e.statsFor(e.ispHop.Host)
		snap.ISPHop = &model.TargetStats{Name: e.ispHop.Name, Host: e.ispHop.Host, Role: model.RoleISPHop, Stats: st, Alive: alive}
	}
	for _, a := range e.cfg.Anchors {
		st, alive := e.statsFor(a.Host)
		snap.Internet = append(snap.Internet, model.TargetStats{Name: a.Name, Host: a.Host, Role: model.RoleInternet, Stats: st, Alive: alive})
	}
	return snap
}

func (e *Engine) publish(snap model.Snapshot) {
	e.mu.Lock()
	e.latest = snap
	subs := e.subs
	e.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- snap:
		default: // drop for slow consumers
		}
	}
}

// refreshTopology re-discovers the gateway and first public (ISP) hop.
func (e *Engine) refreshTopology(ctx context.Context) {
	info, err := netinfo.Collect()
	if err != nil {
		return
	}
	var gw *Target
	if info.GatewayIP != nil {
		gw = &Target{Name: "Gateway", Host: info.GatewayIP.String(), Role: model.RoleGateway}
	}

	e.mu.Lock()
	e.netInfo = info
	e.gateway = gw
	e.mu.Unlock()

	// Discover the ISP edge by tracing toward a public anchor.
	if len(e.cfg.Anchors) > 0 {
		dest := net.ParseIP(e.cfg.Anchors[0].Host)
		if dest != nil {
			hops, _ := probe.Traceroute(ctx, e.pinger, dest, e.cfg.MaxTraceHops, e.cfg.Timeout)
			if h := probe.FirstPublicHop(hops); h != nil {
				e.mu.Lock()
				e.ispHop = &Target{Name: "ISP hop", Host: h.Addr.String(), Role: model.RoleISPHop}
				e.mu.Unlock()
			}
		}
	}
}

func (e *Engine) refreshDNS(ctx context.Context) {
	res := dnsprobe.Measure(ctx, nil, 2*time.Second)
	e.mu.Lock()
	e.dns = res
	e.mu.Unlock()
}

// RunBufferbloat executes an on-demand bufferbloat test and stores the result so
// it appears in subsequent snapshots.
func (e *Engine) RunBufferbloat(ctx context.Context, opt bufferbloat.Options) (bufferbloat.Result, error) {
	res, err := bufferbloat.Run(ctx, e.pinger, opt)
	if err == nil {
		e.mu.Lock()
		r := res
		e.lastBB = &r
		e.mu.Unlock()
	}
	return res, err
}
