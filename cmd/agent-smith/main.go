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
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("agent-smith %s\n", version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *bb {
		if err := runBufferbloat(ctx); err != nil {
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
