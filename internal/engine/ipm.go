package engine

import (
	"context"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/asn"
	"github.com/NYBaywatch/agent-smith/internal/baseline"
	"github.com/NYBaywatch/agent-smith/internal/bgp"
	"github.com/NYBaywatch/agent-smith/internal/classifier"
	"github.com/NYBaywatch/agent-smith/internal/dnsprobe"
	"github.com/NYBaywatch/agent-smith/internal/incident"
	"github.com/NYBaywatch/agent-smith/internal/model"
	"github.com/NYBaywatch/agent-smith/internal/pathmon"
	"github.com/NYBaywatch/agent-smith/internal/synth"
)

// IPMConfig tunes the internet-performance-monitoring loops layered on top
// of the core ring probes: synthetic HTTP checks, hop-by-hop path tracing,
// authoritative DNS timing, BGP visibility, long-term baselines and incident
// grouping.
type IPMConfig struct {
	Synthetics        []synth.Check // HTTP checks to run (nil disables)
	SyntheticInterval time.Duration // cadence for the whole check set
	SyntheticTimeout  time.Duration // per-check timeout
	SyntheticWindow   int           // rolling results kept per check

	PathEnabled  bool
	PathInterval time.Duration // how often every anchor is traced
	ProbesPerHop int

	DNSAuthInterval time.Duration // authoritative nameserver timing cadence
	DNSAuthDomains  []string

	BGPInterval time.Duration // RIPEstat refresh cadence

	SLO              baseline.SLO
	IncidentCooldown time.Duration // healthy time before an open incident closes
	Notify           bool          // UIs may raise desktop notifications on incidents
}

// DefaultIPMConfig returns production-sensible IPM defaults with the built-in
// synthetic presets enabled.
func DefaultIPMConfig() IPMConfig {
	return IPMConfig{
		Synthetics:        synth.Presets(),
		SyntheticInterval: 60 * time.Second,
		SyntheticTimeout:  10 * time.Second,
		SyntheticWindow:   20,
		PathEnabled:       true,
		PathInterval:      3 * time.Minute,
		ProbesPerHop:      3,
		DNSAuthInterval:   5 * time.Minute,
		DNSAuthDomains:    []string{"google.com", "cloudflare.com", "github.com"},
		BGPInterval:       time.Hour,
		SLO:               baseline.SLO{Availability: 0.999, P95Ms: 100},
		IncidentCooldown:  45 * time.Second,
		Notify:            true,
	}
}

// ipm holds the IPM collectors' state inside the engine.
type ipm struct {
	cfg      IPMConfig
	synth    *synth.Monitor
	paths    *pathmon.Monitor
	asn      *asn.Resolver
	tracker  *baseline.Tracker
	grouper  *incident.Grouper
	notifyCh chan incident.Event

	mu       sync.RWMutex
	bgp      *bgp.Status
	dnsAuth  []dnsprobe.AuthResult
	pathDiag pathmon.Diagnosis
	tracing  bool
}

func newIPM(cfg IPMConfig) *ipm {
	return &ipm{
		cfg:      cfg,
		synth:    synth.NewMonitor(cfg.Synthetics, cfg.SyntheticWindow),
		paths:    pathmon.NewMonitor(10),
		asn:      asn.NewResolver(),
		tracker:  baseline.NewTracker(baseline.DefaultRetention),
		grouper:  incident.NewGrouper(cfg.IncidentCooldown, incident.DefaultKeep),
		notifyCh: make(chan incident.Event, 8),
		pathDiag: pathmon.Diagnosis{HopIndex: -1},
	}
}

// Incidents returns every recorded incident, newest first (the open one, if
// any, comes first).
func (e *Engine) Incidents() []incident.Incident { return e.ipm.grouper.List() }

// ClearIncidents forgets recorded incidents and persists the change.
func (e *Engine) ClearIncidents() {
	e.ipm.grouper.Clear()
	e.save()
}

// IncidentEvents delivers incident open/close transitions for UI
// notifications. The channel is buffered and lossy.
func (e *Engine) IncidentEvents() <-chan incident.Event { return e.ipm.notifyCh }

// PathHistory returns recent traceroutes to dest, oldest first.
func (e *Engine) PathHistory(dest string) []pathmon.Path { return e.ipm.paths.History(dest) }

// SyntheticChecks returns the configured synthetic checks.
func (e *Engine) SyntheticChecks() []synth.Check { return e.ipm.synth.Checks() }

// RouteChanges returns recent detected route changes, newest first.
func (e *Engine) RouteChanges() []pathmon.RouteChange { return e.ipm.paths.Changes() }

// LongTerm builds SLA entries for every series with persisted history,
// without needing a live snapshot (used by the --report mode).
func (e *Engine) LongTerm(now time.Time) []model.SLAEntry {
	names := map[string]string{"internet": "Internet", "isp": "ISP hop", "gateway": "Gateway"}
	for _, c := range e.ipm.synth.Checks() {
		names["http:"+c.URL] = c.Name
	}
	slo := e.ipm.cfg.SLO
	availOnly := baseline.SLO{Availability: slo.Availability}
	tr := e.ipm.tracker
	var out []model.SLAEntry
	for _, key := range tr.Keys() {
		name := names[key]
		kind := "ping"
		if strings.HasPrefix(key, "http:") {
			kind = "http"
			if name == "" {
				name = strings.TrimPrefix(key, "http:")
			}
		}
		if name == "" {
			name = key
		}
		s := availOnly
		if key == "internet" {
			s = slo
		}
		en := model.SLAEntry{Key: key, Name: name, Kind: kind}
		en.Hour = tr.Summary(key, time.Hour, now)
		en.Day = tr.Summary(key, 24*time.Hour, now)
		en.Week = tr.Summary(key, 7*24*time.Hour, now)
		en.Baseline = tr.Baseline(key, now)
		en.Compliance = baseline.Evaluate(s, en.Day)
		out = append(out, en)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "ping"
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// NotifyEnabled reports whether UIs should raise desktop notifications when
// incidents open and close.
func (e *Engine) NotifyEnabled() bool { return e.ipm.cfg.Notify }

// SLO returns the objective SLA compliance is judged against.
func (e *Engine) SLO() baseline.SLO { return e.ipm.cfg.SLO }

// RunSyntheticsNow executes every synthetic check immediately (blocking) and
// returns the fresh summaries. Used by the UIs' "check now" actions.
func (e *Engine) RunSyntheticsNow(ctx context.Context) []synth.Summary {
	e.runSynthetics(ctx)
	return e.ipm.synth.Summaries()
}

// TraceNow runs an on-demand traceroute to host and records it like the
// periodic loop would.
func (e *Engine) TraceNow(ctx context.Context, name, host string) (pathmon.Path, error) {
	ip := net.ParseIP(host)
	if ip == nil {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return pathmon.Path{}, err
		}
		if len(addrs) == 0 {
			return pathmon.Path{}, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
		}
		ip = addrs[0].IP
	}
	p, err := pathmon.Trace(ctx, e.pinger, name, ip, e.pathOptions())
	if err == nil {
		e.recordPath(p)
	}
	return p, err
}

func (e *Engine) pathOptions() pathmon.Options {
	opt := pathmon.DefaultOptions()
	opt.ProbesPerHop = e.ipm.cfg.ProbesPerHop
	opt.PerHop = e.cfg.Timeout
	opt.Enrich = true
	opt.ASN = e.ipm.asn
	return opt
}

// --- loops ---

// runIPM starts the IPM loops; it returns when ctx is cancelled.
func (e *Engine) runIPM(ctx context.Context) {
	c := e.ipm.cfg
	var wg sync.WaitGroup
	start := func(delay, every time.Duration, fn func(context.Context)) {
		if every <= 0 {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
				fn(ctx)
			}
			e.periodic(ctx, every, fn)
		}()
	}
	if len(c.Synthetics) > 0 {
		start(3*time.Second, c.SyntheticInterval, e.runSynthetics)
	}
	if c.PathEnabled {
		start(8*time.Second, c.PathInterval, e.tracePaths)
	}
	if len(c.DNSAuthDomains) > 0 {
		start(20*time.Second, c.DNSAuthInterval, e.refreshDNSAuth)
	}
	start(15*time.Second, c.BGPInterval, e.refreshBGP)
	wg.Wait()
}

func (e *Engine) runSynthetics(ctx context.Context) {
	c := e.ipm.cfg
	before := map[string]int{}
	for _, s := range e.ipm.synth.Summaries() {
		before[s.Check.URL] = s.Stats.Runs
	}
	e.ipm.synth.RunAll(ctx, c.SyntheticTimeout, 4)
	// Feed the newest result of each check into the long-term tracker.
	for _, s := range e.ipm.synth.Summaries() {
		if s.Stats.Runs == 0 || s.Stats.Runs == before[s.Check.URL] {
			continue
		}
		last := s.Stats.Last
		e.ipm.tracker.Add("http:"+s.Check.URL, last.When, last.OK, ms(last.Timing.Total))
	}
}

func (e *Engine) tracePaths(ctx context.Context) {
	e.ipm.mu.Lock()
	if e.ipm.tracing {
		e.ipm.mu.Unlock()
		return
	}
	e.ipm.tracing = true
	e.ipm.mu.Unlock()
	defer func() {
		e.ipm.mu.Lock()
		e.ipm.tracing = false
		e.ipm.mu.Unlock()
	}()

	for _, a := range e.cfg.Anchors {
		if ctx.Err() != nil {
			return
		}
		ip := net.ParseIP(a.Host)
		if ip == nil {
			continue
		}
		tctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		p, err := pathmon.Trace(tctx, e.pinger, a.Name, ip, e.pathOptions())
		cancel()
		if err != nil && len(p.Hops) == 0 {
			continue
		}
		e.recordPath(p)
	}
}

// recordPath stores a traceroute, notes route changes and refreshes the
// path diagnosis for the primary anchor.
func (e *Engine) recordPath(p pathmon.Path) {
	e.ipm.paths.Add(p)
	if len(e.cfg.Anchors) > 0 && p.Dest == e.cfg.Anchors[0].Host {
		d := pathmon.FirstDegradedHop(p, classifier.PathLossThreshold, classifier.PathJumpThreshold)
		e.ipm.mu.Lock()
		e.ipm.pathDiag = d
		e.ipm.mu.Unlock()
	}
}

func (e *Engine) refreshDNSAuth(ctx context.Context) {
	res := dnsprobe.MeasureAuthoritative(ctx, e.ipm.cfg.DNSAuthDomains, 2*time.Second)
	e.ipm.mu.Lock()
	e.ipm.dnsAuth = res
	e.ipm.mu.Unlock()
}

func (e *Engine) refreshBGP(ctx context.Context) {
	e.mu.RLock()
	conn := e.conn
	e.mu.RUnlock()
	if conn == nil || conn.IP == "" {
		return
	}
	st, err := bgp.Fetch(ctx, conn.IP)
	if err != nil {
		return
	}
	e.ipm.mu.Lock()
	e.ipm.bgp = st
	e.ipm.mu.Unlock()
}

// --- per-tick hooks ---

// trackTick feeds the ring RTTs into the long-term tracker. Each tick
// contributes one sample per ring: the window mean when the target is alive,
// a failure when it is not.
func (e *Engine) trackTick(snap model.Snapshot) {
	add := func(key string, ts *model.TargetStats) {
		if ts == nil || ts.Stats.Sent == 0 {
			return
		}
		e.ipm.tracker.Add(key, snap.Time, ts.Alive, ms(ts.Stats.Mean))
	}
	add("gateway", snap.Gateway)
	add("isp", snap.ISPHop)
	if b := bestInternetTS(snap); b != nil {
		e.ipm.tracker.Add("internet", snap.Time, true, ms(b.Stats.Mean))
	} else if len(snap.Internet) > 0 && snap.Internet[0].Stats.Sent > 0 {
		e.ipm.tracker.Add("internet", snap.Time, false, 0)
	}
}

// observeIncident folds the verdict into the incident grouper and forwards
// transitions to the notification channel.
func (e *Engine) observeIncident(snap model.Snapshot) {
	ev := e.ipm.grouper.Observe(snap.Time, snap.Verdict)
	if ev.Opened == nil && ev.Closed == nil {
		return
	}
	select {
	case e.ipm.notifyCh <- ev:
	default:
	}
	if ev.Closed != nil {
		go e.save()
	}
}

// fillIPM attaches the IPM state to a snapshot being built. Called without
// e.mu held (the IPM collectors have their own locks).
func (e *Engine) fillIPM(snap *model.Snapshot) {
	snap.Synthetics = e.ipm.synth.Summaries()
	snap.Paths = e.ipm.paths.Latest()
	snap.RouteChanges = e.ipm.paths.Changes()

	e.ipm.mu.RLock()
	snap.BGP = e.ipm.bgp
	snap.DNSAuth = e.ipm.dnsAuth
	snap.PathDiag = e.ipm.pathDiag
	e.ipm.mu.RUnlock()

	snap.SLA = e.slaEntries(*snap)

	if cur := e.ipm.grouper.Current(); cur != nil {
		snap.Incident = &model.IncidentRef{ID: cur.ID, Start: cur.Start, Culprit: cur.Culprit, Peak: cur.Peak, Ticks: cur.Ticks, Headline: cur.Headline}
	}
	snap.AlertRaw, snap.AlertIncidents = e.ipm.grouper.Stats()
}

// slaEntries builds the long-term view for the ring series and every
// synthetic check that has run.
func (e *Engine) slaEntries(snap model.Snapshot) []model.SLAEntry {
	now := snap.Time
	tr := e.ipm.tracker
	slo := e.ipm.cfg.SLO
	availOnly := baseline.SLO{Availability: slo.Availability} // no RTT objective for LAN/ISP/HTTP

	entry := func(key, name, kind string, cur float64, s baseline.SLO) model.SLAEntry {
		en := model.SLAEntry{Key: key, Name: name, Kind: kind, CurrentMs: cur}
		en.Hour = tr.Summary(key, time.Hour, now)
		en.Day = tr.Summary(key, 24*time.Hour, now)
		en.Week = tr.Summary(key, 7*24*time.Hour, now)
		en.Baseline = tr.Baseline(key, now)
		en.Compliance = baseline.Evaluate(s, en.Day)
		if cur > 0 {
			en.Z, en.Anomalous = en.Baseline.Score(cur)
		}
		return en
	}

	var out []model.SLAEntry
	out = append(out, entry("internet", "Internet", "ping", bestInternetMs(snap), slo))
	if snap.ISPHop != nil {
		out = append(out, entry("isp", "ISP hop", "ping", rttMs(snap.ISPHop), availOnly))
	}
	if snap.Gateway != nil {
		out = append(out, entry("gateway", "Gateway", "ping", rttMs(snap.Gateway), availOnly))
	}
	syn := append([]synth.Summary(nil), snap.Synthetics...)
	sort.SliceStable(syn, func(i, j int) bool { return syn[i].Check.Category < syn[j].Check.Category })
	for _, s := range syn {
		if s.Stats.Runs == 0 {
			continue
		}
		cur := 0.0
		if s.Stats.Last.OK {
			cur = ms(s.Stats.Last.Timing.Total)
		}
		out = append(out, entry("http:"+s.Check.URL, s.Check.Name, "http", cur, availOnly))
	}
	return out
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
