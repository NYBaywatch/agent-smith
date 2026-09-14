package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/engine"
	"github.com/NYBaywatch/agent-smith/internal/incident"
	"github.com/NYBaywatch/agent-smith/internal/model"
	"github.com/NYBaywatch/agent-smith/internal/pathmon"
	"github.com/NYBaywatch/agent-smith/internal/synth"
)

// palette returns ANSI codes, or empty strings when colour is off (one-shot
// output that may be piped to a file).
type palette struct{ reset, bold, dim, red, green, yellow, cyan, gray string }

func colours(on bool) palette {
	if !on {
		return palette{}
	}
	return palette{reset, bold, dim, red, green, yellow, cyan, gray}
}

// FormatSynthetics renders the synthetic HTTP checks as a table. limit caps
// the rows (0 = all); failing and slow checks are listed first so they are
// never cut off.
func FormatSynthetics(sums []synth.Summary, colour bool, limit int) string {
	c := colours(colour)
	var b strings.Builder
	if len(sums) == 0 {
		return "  (no synthetic checks configured)\n"
	}
	ordered := make([]synth.Summary, 0, len(sums))
	for _, s := range sums {
		if s.Stats.Down() || s.Stats.Slow() {
			ordered = append(ordered, s)
		}
	}
	for _, s := range sums {
		if !(s.Stats.Down() || s.Stats.Slow()) {
			ordered = append(ordered, s)
		}
	}
	up, down, pending := 0, 0, 0
	for _, s := range sums {
		switch {
		case s.Stats.Runs == 0:
			pending++
		case s.Stats.Down():
			down++
		default:
			up++
		}
	}
	fmt.Fprintf(&b, "%s  Services   %s%d up%s  %s%d down%s  %s%d pending%s\n", c.bold, c.green, up, c.reset+c.bold, c.red, down, c.reset+c.bold, c.gray, pending, c.reset)
	fmt.Fprintf(&b, "%s    %-22s %-8s %-7s %-8s %-8s %-6s %s%s\n", c.dim, "Check", "Category", "Status", "TTFB", "Total", "Avail", "Edge / error", c.reset)
	for i, s := range ordered {
		if limit > 0 && i >= limit {
			fmt.Fprintf(&b, "    %s… %d more%s\n", c.gray, len(ordered)-i, c.reset)
			break
		}
		st := s.Stats
		if st.Runs == 0 {
			fmt.Fprintf(&b, "    %-22s %-8s %s%-7s%s\n", trunc(s.Check.Name, 22), s.Check.Category, c.gray, "pending", c.reset)
			continue
		}
		status, col := "ok", c.green
		switch {
		case st.Down():
			status, col = "DOWN", c.red
		case !st.Last.OK:
			status, col = "fail", c.yellow
		case st.Slow():
			status, col = "slow", c.yellow
		}
		note := st.Last.Edge
		if !st.Last.OK {
			note = st.Last.Err
			if note == "" && st.Last.Status > 0 {
				note = fmt.Sprintf("HTTP %d", st.Last.Status)
			}
		}
		fmt.Fprintf(&b, "    %-22s %-8s %s%-7s%s %-8s %-8s %5.1f%%  %s\n",
			trunc(s.Check.Name, 22), s.Check.Category, col, status, c.reset,
			msStr(st.MeanTTFB), msStr(st.Mean), st.Availability*100, trunc(note, 28))
	}
	return b.String()
}

// FormatPath renders one traceroute with per-hop stats and the degraded-hop
// reading.
func FormatPath(p pathmon.Path, colour bool) string {
	c := colours(colour)
	var b strings.Builder
	title := p.Name
	if title == "" || title == p.Dest {
		title = p.Dest
	} else {
		title += " (" + p.Dest + ")"
	}
	reached := c.green + "reached" + c.reset
	if !p.Reached {
		reached = c.red + "not reached" + c.reset
	}
	fmt.Fprintf(&b, "%s  Route to %s%s  %s  %d hops  %s\n", c.bold, title, c.reset, reached, len(p.Hops), c.gray+p.When.Format("15:04:05")+c.reset)
	fmt.Fprintf(&b, "%s    %-3s %-16s %-24s %-8s %-8s %-6s %s%s\n", c.dim, "hop", "address", "host / AS", "avg", "max", "loss", "seg", c.reset)
	segs := pathmon.Segments(p)
	for i, h := range p.Hops {
		seg := ""
		if i < len(segs) {
			seg = segs[i].String()
		}
		if !h.Responded() {
			fmt.Fprintf(&b, "    %-3d %s%-16s%s %-24s %-8s %-8s %-6s %s\n", h.TTL, c.gray, "*", c.reset, "", "", "", "", seg)
			continue
		}
		who := h.Host
		if h.ASName != "" {
			who = fmt.Sprintf("AS%d %s", h.ASN, h.ASName)
		} else if h.ASN > 0 {
			who = fmt.Sprintf("AS%d", h.ASN)
		} else if h.Private {
			who = "private"
		}
		lossC := c.green
		if h.Loss > 0 {
			lossC = c.yellow
		}
		if h.Loss >= 0.5 {
			lossC = c.red
		}
		fmt.Fprintf(&b, "    %-3d %-16s %-24s %-8s %-8s %s%5.0f%%%s %s\n",
			h.TTL, trunc(h.Addr, 16), trunc(who, 24), msStr(h.Avg), msStr(h.Max), lossC, h.Loss*100, c.reset, seg)
	}
	if as := p.ASSignature(); as != "" {
		fmt.Fprintf(&b, "    %sAS path: %s%s\n", c.gray, as, c.reset)
	}
	return b.String()
}

// FormatDiagnosis renders the first-degraded-hop reading for a path.
func FormatDiagnosis(p pathmon.Path, d pathmon.Diagnosis, colour bool) string {
	c := colours(colour)
	if d.HopIndex < 0 || d.HopIndex >= len(p.Hops) {
		return fmt.Sprintf("    %s✓ path is clean — no hop introduces persistent loss or latency%s\n", c.green, c.reset)
	}
	h := p.Hops[d.HopIndex]
	return fmt.Sprintf("    %s⚠ %s at hop %d (%s) — %s segment%s\n", c.yellow, d.Reason, h.TTL, h.Addr, d.Segment, c.reset)
}

// FormatSLA renders the long-term availability / latency table.
func FormatSLA(entries []model.SLAEntry, colour bool, limit int) string {
	c := colours(colour)
	var b strings.Builder
	fmt.Fprintf(&b, "%s    %-22s %-8s %-8s %-8s %-9s %-10s %s%s\n", c.dim, "Series", "avail 1h", "avail24h", "p95 24h", "baseline", "budget", "SLO", c.reset)
	for i, e := range entries {
		if limit > 0 && i >= limit {
			fmt.Fprintf(&b, "    %s… %d more%s\n", c.gray, len(entries)-i, c.reset)
			break
		}
		if e.Day.Samples == 0 {
			continue
		}
		slo, col := "ok", c.green
		if !e.Compliance.OK {
			slo, col = "BREACH", c.red
		}
		base := "—"
		if e.Baseline.Valid {
			base = fmt.Sprintf("%.0f ms", e.Baseline.Median)
		}
		anom := ""
		if e.Anomalous {
			anom = c.yellow + fmt.Sprintf("  ▲ %.0f ms now (z=%.0f)", e.CurrentMs, e.Z) + c.reset
		}
		fmt.Fprintf(&b, "    %-22s %7.2f%% %7.2f%% %-8s %-9s %-10s %s%s%s%s\n",
			trunc(e.Name, 22), e.Hour.Availability*100, e.Day.Availability*100,
			fmtMs(e.Day.P95), base, budgetStr(e.Compliance.ErrorBudgetLeft), col, slo, c.reset, anom)
	}
	return b.String()
}

// FormatIncidents renders grouped incidents, newest first.
func FormatIncidents(list []incident.Incident, now time.Time, colour bool, limit int) string {
	c := colours(colour)
	var b strings.Builder
	if len(list) == 0 {
		return "    (no incidents recorded)\n"
	}
	for i, in := range list {
		if limit > 0 && i >= limit {
			fmt.Fprintf(&b, "    %s… %d more%s\n", c.gray, len(list)-i, c.reset)
			break
		}
		state, col := "closed", c.gray
		if in.Open() {
			state, col = "OPEN", c.red
		}
		fmt.Fprintf(&b, "    #%-3d %s%-6s%s %s  %-8s %-16s %s %s(%d ticks)%s\n",
			in.ID, col, state, c.reset, in.Start.Format("01-02 15:04"), durStr(in.Duration(now)),
			trunc(in.Culprit.String(), 16), trunc(in.Headline, 40), c.gray, in.Ticks, c.reset)
	}
	return b.String()
}

// FormatReport prints the persisted long-term view without live loops.
func FormatReport(e *engine.Engine, now time.Time) string {
	var b strings.Builder
	b.WriteString("AGENT SMITH — internet performance report  " + now.Format("2006-01-02 15:04") + "\n\n")
	slo := e.SLO()
	fmt.Fprintf(&b, "  SLO: availability %.3f%%", slo.Availability*100)
	if slo.P95Ms > 0 {
		fmt.Fprintf(&b, ", internet p95 ≤ %.0f ms", slo.P95Ms)
	}
	b.WriteString("  (24 h window)\n\n  SLA / baselines\n")
	entries := e.LongTerm(now)
	if len(entries) == 0 {
		b.WriteString("    (no history yet — run the monitor for a while first)\n")
	} else {
		b.WriteString(FormatSLA(entries, false, 0))
	}
	b.WriteString("\n  Incidents\n")
	list := e.Incidents()
	raw, n := 0, 0
	for _, in := range list {
		raw += in.Ticks
		n++
	}
	b.WriteString(FormatIncidents(list, now, false, 25))
	if n > 0 {
		fmt.Fprintf(&b, "    alert compression: %d degraded ticks → %d incidents\n", raw, n)
	}
	b.WriteString("\n  Route changes\n")
	rc := e.RouteChanges()
	if len(rc) == 0 {
		b.WriteString("    (none recorded)\n")
	}
	for i, ch := range rc {
		if i >= 10 {
			break
		}
		fmt.Fprintf(&b, "    %s  %-12s hops %d→%d  %s → %s\n", ch.When.Format("01-02 15:04"), trunc(ch.Name, 12), ch.HopsBefore, ch.HopsAfter, nzs(ch.ASBefore), nzs(ch.ASAfter))
	}
	return b.String()
}

// renderIPM appends the live-dashboard IPM sections.
func renderIPM(b *strings.Builder, s model.Snapshot) {
	// Open incident + compression.
	if s.Incident != nil {
		fmt.Fprintf(b, "  %s%s● INCIDENT #%d%s  since %s (%s)  %s  %s%d degraded ticks%s\n",
			red, bold, s.Incident.ID, reset, s.Incident.Start.Format("15:04:05"), durStr(s.Time.Sub(s.Incident.Start)),
			s.Incident.Headline, gray, s.Incident.Ticks, reset)
	} else if s.AlertIncidents > 0 {
		fmt.Fprintf(b, "  %sAlerts: %d degraded ticks folded into %d incidents%s\n", gray, s.AlertRaw, s.AlertIncidents, reset)
	}

	// Synthetics.
	if len(s.Synthetics) > 0 {
		b.WriteString("\n")
		b.WriteString(FormatSynthetics(s.Synthetics, true, 6))
	}

	// Route to the primary anchor.
	if len(s.Paths) > 0 {
		p := s.Paths[0]
		b.WriteString("\n")
		b.WriteString(bold + "  Route      " + reset)
		fmt.Fprintf(b, "%s → %s  %d hops  AS path %s\n", "you", p.Name, len(p.Hops), nzs(p.ASSignature()))
		b.WriteString(FormatDiagnosis(p, s.PathDiag, true))
		if len(s.RouteChanges) > 0 {
			ch := s.RouteChanges[0]
			fmt.Fprintf(b, "    %slast route change %s to %s (hops %d→%d)%s\n", yellow, ch.When.Format("15:04"), ch.Name, ch.HopsBefore, ch.HopsAfter, reset)
		}
	}

	// BGP + authoritative DNS, one line each.
	if s.BGP != nil {
		col := green
		if !s.BGP.Healthy() {
			col = yellow
		}
		fmt.Fprintf(b, "  BGP        prefix %s via AS%d, %sseen by %.0f%% of route collectors%s\n", nzs(s.BGP.Prefix), s.BGP.OriginASN, col, s.BGP.Visibility*100, reset)
	}
	if len(s.DNSAuth) > 0 {
		var parts []string
		for _, a := range s.DNSAuth {
			if a.OK {
				parts = append(parts, fmt.Sprintf("%s %s", a.Domain, msStr(a.Latency)))
			} else {
				parts = append(parts, red+a.Domain+" fail"+reset)
			}
			if len(parts) == 4 {
				break
			}
		}
		fmt.Fprintf(b, "  Auth DNS   %s\n", strings.Join(parts, "  ·  "))
	}

	// SLA for the rings.
	if len(s.SLA) > 0 {
		b.WriteString("\n" + bold + "  SLA (24 h)\n" + reset)
		b.WriteString(FormatSLA(s.SLA, true, 4))
	}
}

func msStr(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f ms", float64(d)/float64(time.Millisecond))
}

func fmtMs(ms float64) string {
	if ms <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f ms", ms)
}

func budgetStr(left float64) string {
	if left < 0 {
		return "exhausted"
	}
	return fmt.Sprintf("%.0f%% left", left*100)
}

func durStr(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func nzs(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
