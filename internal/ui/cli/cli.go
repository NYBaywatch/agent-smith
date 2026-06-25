// Package cli renders a live terminal dashboard from the engine's snapshot
// stream. It works on any platform (no display required), which also makes it
// the surface CI can exercise.
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/engine"
	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/model"
)

// ANSI helpers.
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
	gray   = "\033[90m"
)

// Run subscribes to the engine and redraws the dashboard on each snapshot until
// ctx is cancelled.
func Run(ctx context.Context, e *engine.Engine) error {
	ch := e.Subscribe()
	fmt.Print("\033[2J") // clear once
	// Draw an initial frame so the user sees something before the first tick.
	render(e.Latest())
	for {
		select {
		case <-ctx.Done():
			fmt.Print(reset + "\n")
			return nil
		case snap := <-ch:
			render(snap)
		}
	}
}

func render(s model.Snapshot) {
	var b strings.Builder
	b.WriteString("\033[H") // home cursor

	v := s.Verdict
	b.WriteString(bold + cyan + "  AGENT SMITH " + reset + dim + " network & system performance monitor" + reset + "\n")
	b.WriteString(gray + "  " + s.Time.Format("15:04:05") + reset + "\n\n")

	// Verdict banner.
	sevColor := severityColor(v.Severity)
	b.WriteString(fmt.Sprintf("  %s%s %s %s%s\n", sevColor, bold, badge(v.Severity), v.Headline, reset))
	if v.Detail != "" {
		b.WriteString("  " + gray + v.Detail + reset + "\n")
	}
	if v.Fix != "" {
		b.WriteString("  " + yellow + "Fix: " + reset + v.Fix + "\n")
	}
	if v.Culprit != model.CulpritHealthy {
		b.WriteString(fmt.Sprintf("  %sLikely culprit: %s%s%s  (confidence %.0f%%)\n",
			dim, sevColor, v.Culprit, reset, v.Confidence*100))
	}
	b.WriteString("\n")

	// Path rings.
	b.WriteString(bold + "  Path     Target              avg      p95      jitter   loss\n" + reset)
	if s.Gateway != nil {
		b.WriteString(ringRow("LAN", s.Gateway))
	}
	if s.ISPHop != nil {
		b.WriteString(ringRow("ISP", s.ISPHop))
	}
	for i := range s.Internet {
		label := "NET"
		if i > 0 {
			label = "   "
		}
		b.WriteString(ringRow(label, &s.Internet[i]))
	}
	b.WriteString("\n")

	// Local diagnostics.
	b.WriteString(bold + "  Local\n" + reset)
	if s.Net != nil && s.Net.Active != nil {
		a := s.Net.Active
		b.WriteString(fmt.Sprintf("    Interface  %s (%s, %d Mbps, MTU %d)\n", a.Name, a.Media, a.LinkMbps, a.MTU))
		if errs := a.InErrors + a.OutErrors + a.InDiscards + a.OutDiscards; errs > 0 {
			b.WriteString(fmt.Sprintf("    NIC errors %s%d%s\n", yellow, errs, reset))
		}
	}
	if s.Net != nil && s.Net.WiFi != nil {
		w := s.Net.WiFi
		b.WriteString(fmt.Sprintf("    Wi-Fi      %q  %d dBm (%d%%)  rx %.0f / tx %.0f Mbps\n",
			w.SSID, w.RSSI, w.SignalQuality, w.RxMbps, w.TxMbps))
	}
	b.WriteString(fmt.Sprintf("    CPU %5.1f%%   Mem %5.1f%%   net ↓%.1f ↑%.1f Mbps\n",
		s.Sys.CPUPercent, s.Sys.MemPercent, s.Sys.InMbps, s.Sys.OutMbps))
	dnsColor := green
	if s.DNS.Slow() {
		dnsColor = yellow
	}
	if s.DNS.Lookups > 0 {
		b.WriteString(fmt.Sprintf("    DNS        %s%s%s avg\n", dnsColor, round(s.DNS.Avg), reset))
	}
	if s.Bufferbloat != nil {
		bb := s.Bufferbloat
		b.WriteString(fmt.Sprintf("    Bufferbloat grade %s%s%s (+%s under load, %.0f Mbps down)\n",
			gradeColor(bb.Grade), bb.Grade, reset, round(bb.Added), bb.DownloadMbps))
	}

	b.WriteString("\n" + gray + "  Ctrl-C to quit" + reset + "\033[J\n")
	fmt.Print(b.String())
}

func ringRow(label string, ts *model.TargetStats) string {
	if ts == nil {
		return ""
	}
	if !ts.Alive {
		return fmt.Sprintf("  %-3s      %-18s  %s%s%s\n", label, trunc(ts.Host, 18), red, "no reply", reset)
	}
	st := ts.Stats
	c := ratingColor(metrics.RateLatency(st.Mean))
	lossC := green
	if st.Loss > 0.01 {
		lossC = yellow
	}
	if st.Loss > 0.025 {
		lossC = red
	}
	return fmt.Sprintf("  %-3s      %-18s  %s%-7s%s  %-7s  %-7s  %s%.1f%%%s\n",
		label, trunc(ts.Name+" "+ts.Host, 18),
		c, round(st.Mean), reset, round(st.P95), round(st.Jitter),
		lossC, st.Loss*100, reset)
}

func badge(s model.Severity) string {
	switch s {
	case model.SevOK:
		return "● OK     "
	case model.SevWatch:
		return "● WATCH  "
	case model.SevDegraded:
		return "● DEGRADED"
	case model.SevCritical:
		return "● CRITICAL"
	default:
		return "●        "
	}
}

func severityColor(s model.Severity) string {
	switch s {
	case model.SevOK:
		return green
	case model.SevWatch:
		return yellow
	case model.SevDegraded, model.SevCritical:
		return red
	default:
		return reset
	}
}

func ratingColor(r metrics.Rating) string {
	switch r {
	case metrics.RatingExcellent, metrics.RatingGood:
		return green
	case metrics.RatingPlayable:
		return yellow
	case metrics.RatingPoor:
		return red
	default:
		return gray
	}
}

func gradeColor(grade string) string {
	switch grade {
	case "A+", "A", "B":
		return green
	case "C":
		return yellow
	default:
		return red
	}
}

func round(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d >= time.Millisecond {
		return d.Round(100 * time.Microsecond)
	}
	return d.Round(10 * time.Microsecond)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
