// Command agent-smith is the entrypoint for the Agent Smith connection-quality
// monitor. It runs a headless live CLI dashboard on any platform and (on
// Windows) a native GUI; it can also run a one-shot bufferbloat test.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/bufferbloat"
	"github.com/NYBaywatch/agent-smith/internal/probe"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cli := flag.Bool("cli", false, "run the headless terminal dashboard instead of the GUI")
	bb := flag.Bool("bufferbloat", false, "run a one-shot bufferbloat test and exit")
	trace := flag.String("trace", "", "run a one-shot enriched traceroute to HOST and exit")
	check := flag.Bool("check", false, "run every synthetic HTTP check once and exit")
	report := flag.Bool("report", false, "print the SLA / baseline / incident report and exit")
	speed := flag.Bool("speedtest", false, "run a one-shot download/upload speed test with latency under load and exit")
	stab := flag.Bool("stability", false, "run a one-shot 200-probe stability burst and exit")
	dnsb := flag.Bool("dnsbench", false, "run a one-shot DNS resolver benchmark and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: agent-smith [flags]\n\n")
		flag.PrintDefaults()
		fmt.Fprintln(flag.CommandLine.Output(), "\n"+usageExtra())
	}
	flag.Parse()

	if *showVersion {
		fmt.Printf("agent-smith %s\n", version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var oneShot func(context.Context) error
	switch {
	case *bb:
		oneShot = runBufferbloat
	case *trace != "":
		oneShot = func(ctx context.Context) error { return runTrace(ctx, *trace) }
	case *check:
		oneShot = runChecks
	case *report:
		oneShot = runReport
	case *speed:
		oneShot = runSpeedTest
	case *stab:
		oneShot = runStability
	case *dnsb:
		oneShot = runDNSBench
	}
	if oneShot != nil {
		if err := oneShot(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "agent-smith:", err)
			os.Exit(1)
		}
		return
	}

	if err := run(ctx, *cli); err != nil {
		fmt.Fprintln(os.Stderr, "agent-smith:", err)
		os.Exit(1)
	}
}

// runBufferbloat performs a standalone bufferbloat measurement.
func runBufferbloat(ctx context.Context) error {
	p, err := probe.New()
	if err != nil {
		return err
	}
	defer p.Close()

	fmt.Println("Running bufferbloat test (saturating download, ~10s)…")
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := bufferbloat.Run(tctx, p, bufferbloat.DefaultOptions())
	if err != nil {
		return err
	}
	fmt.Printf("\n  Idle RTT:    %v\n  Loaded RTT:  %v\n  Added:       %v\n  Grade:       %s\n  Download:    %.1f Mbps\n",
		res.IdleRTT.Round(time.Millisecond), res.LoadedRTT.Round(time.Millisecond),
		res.Added.Round(time.Millisecond), res.Grade, res.DownloadMbps)
	if res.Grade == "C" || res.Grade == "D" || res.Grade == "F" {
		fmt.Println("\n  ⚠ Significant bufferbloat. Enable SQM/QoS on your router — more bandwidth won't help.")
	} else {
		fmt.Println("\n  ✓ Latency stays low under load — healthy for real-time and latency-sensitive workloads.")
	}
	return nil
}
