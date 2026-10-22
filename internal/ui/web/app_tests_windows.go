//go:build windows

package web

import (
	"context"
	"fmt"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/NYBaywatch/agent-smith/internal/speedtest"
)

// On-demand tests bound to the page. Each streams progress as the
// "test-progress" event and returns the result (with an Error string instead
// of a Go error). One test runs at a time.

func (a *App) acquireTest(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.testBusy != "" {
		return false
	}
	a.testBusy = name
	return true
}

func (a *App) releaseTest() {
	a.mu.Lock()
	a.testBusy = ""
	a.mu.Unlock()
}

func (a *App) emitProgress(p TestProgress) {
	if ctx := a.context(); ctx != nil {
		runtime.EventsEmit(ctx, "test-progress", p)
	}
}

// RunSpeedTest measures download/upload throughput and latency under load.
func (a *App) RunSpeedTest() SpeedDTO {
	if !a.acquireTest("speed") {
		return SpeedDTO{Error: "another test is already running"}
	}
	defer a.releaseTest()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := a.eng.RunSpeedTest(ctx, func(p speedtest.Progress) {
		a.emitProgress(TestProgress{Test: "speed", Phase: string(p.Phase), Pct: p.Pct, Mbps: p.Mbps, RTTMs: ms(p.RTT), Detail: p.Detail})
	})
	if err != nil {
		return SpeedDTO{Error: err.Error()}
	}
	return *BuildSpeed(&res)
}

// RunStability fires the 200-probe burst at the primary anchor.
func (a *App) RunStability() StabilityDTO {
	if !a.acquireTest("stability") {
		return StabilityDTO{Error: "another test is already running"}
	}
	defer a.releaseTest()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := a.eng.RunStability(ctx, func(done, total int, rtt time.Duration, ok bool) {
		p := TestProgress{Test: "stability", Phase: "burst", Done: done, Total: total, Detail: stabilityProgressDetail(done, total, rtt, ok)}
		if total > 0 {
			p.Pct = float64(done) / float64(total)
		}
		if ok {
			p.RTTMs = ms(rtt)
		}
		a.emitProgress(p)
	})
	if err != nil {
		return StabilityDTO{Error: err.Error()}
	}
	return *BuildStability(&res)
}

// RunDNSBench races the resolvers.
func (a *App) RunDNSBench() DNSBenchDTO {
	if !a.acquireTest("dns") {
		return DNSBenchDTO{Error: "another test is already running"}
	}
	defer a.releaseTest()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := a.eng.RunDNSBench(ctx, func(done, total int) {
		p := TestProgress{Test: "dns", Phase: "lookup", Done: done, Total: total, Detail: fmt.Sprintf("%d of %d lookups", done, total)}
		if total > 0 {
			p.Pct = float64(done) / float64(total)
		}
		a.emitProgress(p)
	})
	if err != nil {
		return DNSBenchDTO{Error: err.Error()}
	}
	return *BuildDNSBench(&res)
}

// RunBufferbloat executes the latency-under-load test (blocking, ~10 s).
func (a *App) RunBufferbloat() BufferbloatResult {
	if !a.acquireTest("bufferbloat") {
		return BufferbloatResult{Error: "another test is already running"}
	}
	defer a.releaseTest()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	res, err := a.eng.RunBufferbloatWithProgress(ctx, func(phase string, rtt time.Duration) {
		pct := 0.15
		if phase == "load" {
			pct = 0.6
		}
		a.emitProgress(TestProgress{Test: "bufferbloat", Phase: phase, Pct: pct, RTTMs: ms(rtt), Detail: phase + " · " + fmt.Sprintf("%.1f ms", ms(rtt))})
	})
	if err != nil {
		return BufferbloatResult{Error: err.Error()}
	}
	return *BuildBufferbloat(&res)
}
