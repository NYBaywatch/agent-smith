package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/classifier"
	"github.com/NYBaywatch/agent-smith/internal/config"
	"github.com/NYBaywatch/agent-smith/internal/engine"
	"github.com/NYBaywatch/agent-smith/internal/pathmon"
	"github.com/NYBaywatch/agent-smith/internal/synth"
	cliui "github.com/NYBaywatch/agent-smith/internal/ui/cli"
)

// runTrace performs a one-shot enriched traceroute (per-hop loss/RTT, reverse
// DNS, ASN, first-degraded-hop reading) and prints it.
func runTrace(ctx context.Context, host string) error {
	eng, err := engine.New(engineConfig())
	if err != nil {
		return err
	}
	fmt.Printf("Tracing route to %s (3 probes per hop)…\n\n", host)
	tctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	p, err := eng.TraceNow(tctx, host, host)
	if err != nil && len(p.Hops) == 0 {
		return err
	}
	fmt.Print(cliui.FormatPath(p, false))
	fmt.Print(cliui.FormatDiagnosis(p, pathmon.FirstDegradedHop(p, classifier.PathLossThreshold, classifier.PathJumpThreshold), false))
	return nil
}

// runChecks executes every configured synthetic HTTP check once and prints
// the timing breakdown for each.
func runChecks(ctx context.Context) error {
	uc, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-smith: config:", err, "(using defaults)")
	}
	checks := uc.Checks()
	if len(checks) == 0 {
		checks = synth.Presets()
	}
	fmt.Printf("Running %d synthetic checks…\n\n", len(checks))
	m := synth.NewMonitor(checks, 1)
	m.RunAll(ctx, 10*time.Second, 4)
	fmt.Print(cliui.FormatSynthetics(m.Summaries(), false, 0))
	return nil
}

// runReport prints the persisted long-term view: SLA compliance per series,
// baselines, incidents and route changes — without starting the live loops.
func runReport(ctx context.Context) error {
	eng, err := engine.New(engineConfig())
	if err != nil {
		return err
	}
	fmt.Print(cliui.FormatReport(eng, time.Now()))
	return nil
}

func usageExtra() string {
	return strings.TrimSpace(`
One-shot modes:
  --bufferbloat        saturate the link and grade latency under load
  --trace HOST         enriched traceroute (loss, RTT, rDNS, ASN, degraded-hop reading)
  --check              run every synthetic HTTP check once and print timings
  --report             print SLA / baseline / incident report from persisted history
`)
}
