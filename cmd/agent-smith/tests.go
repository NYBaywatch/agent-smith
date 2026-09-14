package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/engine"
	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/speedtest"
)

// One-shot versions of the on-demand tests, with a live progress line.

func newEngine() (*engine.Engine, error) { return engine.New(engineConfig()) }

func progressLine(s string) { fmt.Printf("\r\033[K  %s", s) }

func ratingWord(r metrics.Rating) string {
	switch r {
	case metrics.RatingExcellent:
		return "Excellent"
	case metrics.RatingGood:
		return "Good"
	case metrics.RatingPlayable:
		return "Fair"
	case metrics.RatingPoor:
		return "Poor"
	default:
		return "—"
	}
}

func runSpeedTest(ctx context.Context) error {
	eng, err := newEngine()
	if err != nil {
		return err
	}
	fmt.Println("Speed test: idle baseline, then ~8 s download and ~8 s upload with latency sampling…")
	tctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	res, err := eng.RunSpeedTest(tctx, func(p speedtest.Progress) {
		switch p.Phase {
		case speedtest.PhaseDownload, speedtest.PhaseUpload:
			progressLine(fmt.Sprintf("%-8s %6.1f Mbps   rtt %5.1f ms   %3.0f%%", p.Phase, p.Mbps, float64(p.RTT)/1e6, p.Pct*100))
		default:
			progressLine(fmt.Sprintf("%-8s %s", p.Phase, p.Detail))
		}
	})
	fmt.Print("\r\033[K")
	if err != nil {
		return err
	}
	fmt.Printf("\n  Server:     %s %s\n", res.Source, res.Colo)
	fmt.Printf("  Idle RTT:   %v\n", res.IdleRTT.Round(100*time.Microsecond))
	fmt.Printf("  Download:   %.1f Mbps (peak %.1f)  ·  latency under load %v (+%v, grade %s)  ·  %s\n",
		res.DownMbps, res.DownPeakMbps, res.DownRTT.Round(100*time.Microsecond), res.DownAdded.Round(100*time.Microsecond), res.DownGrade, ratingWord(speedtest.RateThroughput(res.DownMbps)))
	if res.UpBytes > 0 {
		fmt.Printf("  Upload:     %.1f Mbps (peak %.1f)  ·  latency under load %v (+%v, grade %s)  ·  %s\n",
			res.UpMbps, res.UpPeakMbps, res.UpRTT.Round(100*time.Microsecond), res.UpAdded.Round(100*time.Microsecond), res.UpGrade, ratingWord(speedtest.RateThroughput(res.UpMbps)))
	} else {
		fmt.Println("  Upload:     not measured (source has no upload endpoint)")
	}
	fmt.Printf("\n  %s\n", speedtest.Summary(res))
	return nil
}

func runStability(ctx context.Context) error {
	eng, err := newEngine()
	if err != nil {
		return err
	}
	fmt.Println("Stability burst: 200 probes at 50 ms…")
	tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := eng.RunStability(tctx, func(done, total int, rtt time.Duration, ok bool) {
		if ok {
			progressLine(fmt.Sprintf("probe %3d/%d  %6.1f ms", done, total, float64(rtt)/1e6))
		} else {
			progressLine(fmt.Sprintf("probe %3d/%d  lost", done, total))
		}
	})
	fmt.Print("\r\033[K")
	if err != nil {
		return err
	}
	fmt.Printf("\n  Target:  %s (%s)   %d probes in %v\n", res.Name, res.Target, res.Sent, res.Duration.Round(time.Millisecond))
	fmt.Printf("  Loss:    %.1f%%  (%d lost, longest gap %d)\n", res.Loss*100, res.Sent-res.Recv, res.MaxGap)
	fmt.Printf("  RTT:     p50 %v · p95 %v · p99 %v · max %v\n", res.P50.Round(100*time.Microsecond), res.P95.Round(100*time.Microsecond), res.P99.Round(100*time.Microsecond), res.Max.Round(100*time.Microsecond))
	fmt.Printf("  Jitter:  %v (RFC 3550) · %d spikes\n", res.Jitter.Round(100*time.Microsecond), res.Spikes)
	fmt.Printf("\n  %s — %s\n", strings.ToUpper(ratingWord(res.Rating)), res.Verdict)
	if res.Detail != "" {
		fmt.Printf("  %s\n", res.Detail)
	}
	return nil
}

func runDNSBench(ctx context.Context) error {
	eng, err := newEngine()
	if err != nil {
		return err
	}
	fmt.Println("DNS benchmark: racing your resolver against public resolvers…")
	tctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	res, err := eng.RunDNSBench(tctx, func(done, total int) {
		progressLine(fmt.Sprintf("%d of %d lookups", done, total))
	})
	fmt.Print("\r\033[K")
	if err != nil {
		return err
	}
	fmt.Printf("\n  %-4s %-22s %-20s %-9s %-9s %-10s %s\n", "rank", "resolver", "address", "median", "p95", "uncached", "status")
	for _, r := range res.Resolvers {
		addr := r.Addr
		if addr == "" {
			addr = "(system)"
		}
		status := ratingWord(r.Rating)
		if !r.OK {
			status = "FAILED"
		}
		fmt.Printf("  %-4d %-22s %-20s %-9s %-9s %-10s %s\n", r.Rank, r.Name, addr, r.Median.Round(100*time.Microsecond), r.P95.Round(100*time.Microsecond), r.Uncached.Round(100*time.Microsecond), status)
	}
	fmt.Printf("\n  %s\n", res.Recommendation)
	return nil
}
