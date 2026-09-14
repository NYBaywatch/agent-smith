package engine

import (
	"context"
	"net"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/bufferbloat"
	"github.com/NYBaywatch/agent-smith/internal/dnsbench"
	"github.com/NYBaywatch/agent-smith/internal/speedtest"
	"github.com/NYBaywatch/agent-smith/internal/stability"
	"github.com/NYBaywatch/agent-smith/internal/store"
)

// On-demand tests. Each one runs to completion, stores its result so it
// appears in every later snapshot, and persists it so the last result
// survives a restart. Only one test runs at a time (the UIs enforce that;
// the engine simply records whatever finishes).

// LastTests returns the most recent result of every on-demand test (nil when
// a test has never run).
func (e *Engine) LastTests() store.Tests {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return store.Tests{Bufferbloat: e.lastBB, Speed: e.lastSpeed, Stability: e.lastStab, DNSBench: e.lastDNS}
}

// RunSpeedTest measures download and upload throughput with latency under
// load in both directions (~20 s). progress may be nil.
func (e *Engine) RunSpeedTest(ctx context.Context, progress func(speedtest.Progress)) (speedtest.Result, error) {
	opt := speedtest.DefaultOptions()
	if ip := e.primaryAnchorIP(); ip != nil {
		opt.PingTarget = ip
	}
	res, err := speedtest.Run(ctx, e.pinger, opt, progress)
	if err == nil {
		e.mu.Lock()
		r := res
		e.lastSpeed = &r
		e.mu.Unlock()
		e.save()
	}
	return res, err
}

// RunStability fires a dense probe burst (200 × 50 ms) at the healthiest
// internet anchor and grades loss, jitter and outage gaps. progress may be nil.
func (e *Engine) RunStability(ctx context.Context, progress func(done, total int, rtt time.Duration, ok bool)) (stability.Result, error) {
	name, ip := e.primaryAnchor()
	if ip == nil {
		ip, name = net.IPv4(1, 1, 1, 1), "Cloudflare"
	}
	opt := stability.DefaultOptions(ip, name)
	opt.Progress = progress
	res, err := stability.Run(ctx, e.pinger, opt)
	if err == nil {
		e.mu.Lock()
		r := res
		e.lastStab = &r
		e.mu.Unlock()
		e.save()
	}
	return res, err
}

// RunDNSBench races the configured resolver against public resolvers and the
// gateway on real-world names. progress may be nil.
func (e *Engine) RunDNSBench(ctx context.Context, progress func(done, total int)) (dnsbench.Result, error) {
	e.mu.RLock()
	gw := ""
	if e.gateway != nil {
		gw = e.gateway.Host
	}
	e.mu.RUnlock()
	opt := dnsbench.Options{Resolvers: dnsbench.DefaultResolvers(gw), Progress: progress}
	res, err := dnsbench.Run(ctx, opt)
	if err == nil {
		e.mu.Lock()
		r := res
		e.lastDNS = &r
		e.mu.Unlock()
		e.save()
	}
	return res, err
}

// primaryAnchor returns the healthiest alive internet anchor from the latest
// snapshot, falling back to the first configured anchor.
func (e *Engine) primaryAnchor() (string, net.IP) {
	snap := e.Latest()
	if b := bestInternetTS(snap); b != nil {
		return b.Name, net.ParseIP(b.Host)
	}
	if len(e.cfg.Anchors) > 0 {
		return e.cfg.Anchors[0].Name, net.ParseIP(e.cfg.Anchors[0].Host)
	}
	return "", nil
}

func (e *Engine) primaryAnchorIP() net.IP {
	_, ip := e.primaryAnchor()
	return ip
}

// restoreTests loads persisted test results.
func (e *Engine) restoreTests(t store.Tests) {
	e.lastBB, e.lastSpeed, e.lastStab, e.lastDNS = t.Bufferbloat, t.Speed, t.Stability, t.DNSBench
}

// bufferbloatWithProgress runs the bufferbloat test with a progress callback
// and stores the result like RunBufferbloat does.
func (e *Engine) bufferbloatWithProgress(ctx context.Context, progress func(phase string, rtt time.Duration)) (bufferbloat.Result, error) {
	opt := bufferbloat.DefaultOptions()
	opt.Progress = progress
	if ip := e.primaryAnchorIP(); ip != nil {
		opt.PingTarget = ip
	}
	return e.RunBufferbloat(ctx, opt)
}

// RunBufferbloatWithProgress is RunBufferbloat with live RTT reporting.
func (e *Engine) RunBufferbloatWithProgress(ctx context.Context, progress func(phase string, rtt time.Duration)) (bufferbloat.Result, error) {
	return e.bufferbloatWithProgress(ctx, progress)
}
