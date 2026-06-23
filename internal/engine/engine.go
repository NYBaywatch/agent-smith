// Package engine orchestrates Agent Smith's probes and collectors on a schedule
// and produces a stream of model.Snapshot values for the UIs. It pings the
// concentric rings (gateway → ISP edge → public anchors), refreshes local
// topology and DNS latency periodically, samples host resources every tick, and
// runs the classifier to attach a verdict to each snapshot.
package engine

import (
	"context"
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
	"github.com/NYBaywatch/agent-smith/internal/sysinfo"
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

	mu       sync.RWMutex
	windows  map[string]*metrics.Window // keyed by host
	gateway  *Target
	ispHop   *Target
	netInfo  *netinfo.Info
	dns      dnsprobe.Result
	lastBB   *bufferbloat.Result
	latest  model.Snapshot
	subs    []chan model.Snapshot
}

// New constructs an Engine with the given config.
func New(cfg Config) (*Engine, error) {
	p, err := probe.New()
	if err != nil {
		return nil, err
	}
	return &Engine{
		cfg:     cfg,
		pinger:  p,
		sys:     sysinfo.NewCollector(),
		windows: make(map[string]*metrics.Window),
	}, nil
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
	wg.Add(3)
	go func() { defer wg.Done(); e.probeLoop(ctx) }()
	go func() { defer wg.Done(); e.periodic(ctx, e.cfg.TopologyInterval, e.refreshTopology) }()
	go func() { defer wg.Done(); e.periodic(ctx, e.cfg.DNSInterval, e.refreshDNS) }()

	<-ctx.Done()
	wg.Wait()
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
