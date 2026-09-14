//go:build windows

package gui

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"time"

	"github.com/lxn/walk"
	decl "github.com/lxn/walk/declarative"
	"github.com/lxn/win"

	"github.com/NYBaywatch/agent-smith/internal/incident"
	"github.com/NYBaywatch/agent-smith/internal/model"
	"github.com/NYBaywatch/agent-smith/internal/pathmon"
	"github.com/NYBaywatch/agent-smith/internal/synth"
)

// rowModel is a generic string-cell TableView model with a per-row text
// colour, used by the IPM tables (route, services, SLA, incidents). It is
// replaced wholesale when the rendered content changes, never mutated.
type rowModel struct {
	walk.TableModelBase
	rows   [][]string
	colors []walk.Color
}

func (m *rowModel) RowCount() int { return len(m.rows) }

func (m *rowModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) || col < 0 || col >= len(m.rows[row]) {
		return ""
	}
	return m.rows[row][col]
}

// table couples a TableView with its model and a content signature so the
// model is only swapped (and the selection reset) when the rows change.
type table struct {
	tv    *walk.TableView
	model *rowModel
	sig   string
}

func (t *table) set(rows [][]string, colors []walk.Color) {
	if t.tv == nil {
		return
	}
	var b strings.Builder
	for i, r := range rows {
		b.WriteString(strings.Join(r, "\x1f"))
		if i < len(colors) {
			fmt.Fprintf(&b, "\x1e%d", uint32(colors[i]))
		}
		b.WriteByte('\n')
	}
	sig := b.String()
	if sig == t.sig && t.model != nil {
		return
	}
	t.sig = sig
	prev := t.tv.CurrentIndex()
	t.model = &rowModel{rows: rows, colors: colors}
	_ = t.tv.SetModel(t.model)
	if prev >= 0 && prev < len(rows) {
		_ = t.tv.SetCurrentIndex(prev)
	}
}

func (t *table) styleCell(style *walk.CellStyle) {
	style.BackgroundColor = cPanel
	style.TextColor = cText
	if t.model != nil {
		if r := style.Row(); r >= 0 && r < len(t.model.colors) {
			style.TextColor = t.model.colors[r]
		}
	}
}

// ipmUI holds the widgets of the IPM tabs.
type ipmUI struct {
	route      table
	routeHead  *walk.Label
	routeDiag  *walk.Label
	routeOther *walk.Label

	services     table
	servicesHead *walk.Label
	checkButton  *walk.PushButton
	checking     bool

	sla     table
	slaHead *walk.Label

	incidents     table
	incidentsHead *walk.Label

	connPrefix, connBGP *walk.Label
	dnsAuth             [6]struct{ domain, ns, lat, status *walk.Label }
}

// darkenTable paints the list-view surface (including the empty area below
// the rows) in the panel colour. walk applies the Windows theme colour to the
// underlying SysListView32 windows, so send them the dark colours directly.
func darkenTable(tv *walk.TableView) {
	if tv == nil {
		return
	}
	var buf [64]uint16
	for child := win.GetWindow(tv.Handle(), win.GW_CHILD); child != 0; child = win.GetWindow(child, win.GW_HWNDNEXT) {
		n, err := win.GetClassName(child, &buf[0], len(buf))
		if err != nil || syscall.UTF16ToString(buf[:n]) != "SysListView32" {
			continue
		}
		win.SendMessage(child, win.LVM_SETBKCOLOR, 0, uintptr(cPanel))
		win.SendMessage(child, win.LVM_SETTEXTBKCOLOR, 0, uintptr(cPanel))
		win.SendMessage(child, win.LVM_SETTEXTCOLOR, 0, uintptr(cText))
	}
}

// tableDecl builds a dark-styled TableView bound to t.
func tableDecl(t *table, cols []decl.TableViewColumn, minH int) decl.TableView {
	return decl.TableView{
		AssignTo: &t.tv, Background: panelBrush, ColumnsSizable: true, LastColumnStretched: true,
		MinSize: decl.Size{Width: 320, Height: minH}, StretchFactor: 1,
		Columns:   cols,
		StyleCell: t.styleCell,
	}
}

func subLabel(assign **walk.Label, text string, tip string) decl.Label {
	return decl.Label{AssignTo: assign, Text: text, TextColor: cSub, Background: panelBrush,
		Font: decl.Font{Family: "Segoe UI", PointSize: 9}, ToolTipText: tip, MaxSize: decl.Size{Width: 740}}
}

// routeTab builds the hop-by-hop path panel.
func (u *ui) routeTab() []decl.Widget {
	return []decl.Widget{
		subLabel(&u.ipm.routeHead, "Tracing route…", "Hop-by-hop path to the primary internet anchor, re-traced every few minutes with 3 probes per hop. Loss at a middle hop that vanishes downstream is ICMP rate limiting, not real loss."),
		subLabel(&u.ipm.routeDiag, "", "Where degradation is introduced, if anywhere: the first hop whose loss or latency jump persists all the way to the destination."),
		tableDecl(&u.ipm.route, []decl.TableViewColumn{
			{Title: "Hop", Width: 40},
			{Title: "Address", Width: 120},
			{Title: "Host / AS", Width: 190},
			{Title: "Avg", Width: 60},
			{Title: "Max", Width: 60},
			{Title: "Loss", Width: 55},
			{Title: "Segment", Width: 80},
		}, 120),
		subLabel(&u.ipm.routeOther, "", "Other traced destinations and the most recent route change."),
	}
}

// servicesTab builds the synthetic-check panel.
func (u *ui) servicesTab() []decl.Widget {
	return []decl.Widget{
		decl.Composite{Background: panelBrush, Layout: decl.HBox{MarginsZero: true, Spacing: 8}, Children: []decl.Widget{
			subLabel(&u.ipm.servicesHead, "Waiting for the first synthetic run…", "HTTP checks against SaaS, cloud, CDN and AI-API endpoints, run every minute from this machine. TTFB = time to first byte after the request was sent; Edge = which CDN point of presence answered. Add your own in config.json."),
			decl.HSpacer{},
			decl.PushButton{AssignTo: &u.ipm.checkButton, Text: "Check now", OnClicked: u.onCheckNow, ToolTipText: "Run every synthetic check immediately."},
		}},
		tableDecl(&u.ipm.services, []decl.TableViewColumn{
			{Title: "Check", Width: 150},
			{Title: "Category", Width: 65},
			{Title: "Status", Width: 55},
			{Title: "DNS", Width: 55},
			{Title: "TLS", Width: 55},
			{Title: "TTFB", Width: 60},
			{Title: "Total", Width: 60},
			{Title: "Avail", Width: 55},
			{Title: "Edge / error", Width: 150},
		}, 150),
	}
}

// slaTab builds the long-term availability / baseline panel.
func (u *ui) slaTab() []decl.Widget {
	return []decl.Widget{
		subLabel(&u.ipm.slaHead, "Collecting history…", "Availability and p95 latency over the last hour / 24 h / 7 days per monitored series, the 24 h baseline (median), how much of the SLO error budget is left, and whether the current value is anomalous versus this connection's own normal."),
		tableDecl(&u.ipm.sla, []decl.TableViewColumn{
			{Title: "Series", Width: 150},
			{Title: "Avail 1h", Width: 65},
			{Title: "Avail 24h", Width: 70},
			{Title: "Avail 7d", Width: 65},
			{Title: "p95 24h", Width: 65},
			{Title: "Baseline", Width: 70},
			{Title: "Budget", Width: 80},
			{Title: "SLO", Width: 60},
			{Title: "Now", Width: 120},
		}, 150),
	}
}

// incidentsWidgets builds the incident list that sits above the raw events.
func (u *ui) incidentsWidgets() []decl.Widget {
	return []decl.Widget{
		subLabel(&u.ipm.incidentsHead, "No incidents yet.", "Consecutive degraded readings are grouped into one incident (alert compression). An incident closes after 45 s of healthy readings."),
		tableDecl(&u.ipm.incidents, []decl.TableViewColumn{
			{Title: "#", Width: 36},
			{Title: "Start", Width: 90},
			{Title: "Duration", Width: 70},
			{Title: "Where", Width: 110},
			{Title: "Peak", Width: 75},
			{Title: "Ticks", Width: 50},
			{Title: "Incident", Width: 250},
		}, 70),
	}
}

func (u *ui) onCheckNow() {
	if u.ipm.checking {
		return
	}
	u.ipm.checking = true
	u.ipm.checkButton.SetEnabled(false)
	go func() {
		ctx, cancel := context.WithTimeout(u.ctx, 60*time.Second)
		defer cancel()
		u.eng.RunSyntheticsNow(ctx)
		u.mw.Synchronize(func() {
			u.ipm.checking = false
			u.ipm.checkButton.SetEnabled(true)
			u.render(u.eng.Latest())
		})
	}()
}

func (u *ui) onClearIncidents() {
	u.eng.ClearIncidents()
	u.renderIncidents(u.eng.Incidents(), time.Now())
}

// notifyLoop turns incident transitions into tray balloons.
func (u *ui) notifyLoop(ctx context.Context) {
	if !u.eng.NotifyEnabled() {
		return
	}
	ch := u.eng.IncidentEvents()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			u.mw.Synchronize(func() {
				if u.tray == nil {
					return
				}
				if ev.Opened != nil {
					_ = u.tray.ShowWarning("Agent Smith — "+ev.Opened.Culprit.String(), ev.Opened.Headline)
				}
				if ev.Closed != nil {
					_ = u.tray.ShowInfo("Agent Smith — resolved",
						fmt.Sprintf("%s (lasted %s)", ev.Closed.Headline, durText(ev.Closed.Duration(time.Now()))))
				}
			})
		}
	}
}

// renderIPM refreshes every IPM widget from a snapshot.
func (u *ui) renderIPM(s model.Snapshot) {
	u.renderRoute(s)
	u.renderServices(s)
	u.renderSLA(s)
	u.renderIncidents(u.eng.Incidents(), s.Time)
	u.renderBGP(s)
	u.renderDNSAuth(s)
}

func (u *ui) renderRoute(s model.Snapshot) {
	if len(s.Paths) == 0 {
		u.ipm.routeHead.SetText("Tracing route… (first trace runs a few seconds after start)")
		u.ipm.routeDiag.SetText("")
		u.ipm.route.set(nil, nil)
		return
	}
	p := s.Paths[0]
	reached := "reached"
	if !p.Reached {
		reached = "NOT reached"
	}
	u.ipm.routeHead.SetText(fmt.Sprintf("You → %s (%s)   %d hops, %s   AS path %s   traced %s",
		p.Name, p.Dest, len(p.Hops), reached, nz(p.ASSignature()), p.When.Format("15:04:05")))

	d := s.PathDiag
	if d.HopIndex >= 0 && d.HopIndex < len(p.Hops) {
		h := p.Hops[d.HopIndex]
		u.ipm.routeDiag.SetText(fmt.Sprintf("⚠ %s at hop %d (%s) — %s segment", d.Reason, h.TTL, h.Addr, d.Segment))
		u.ipm.routeDiag.SetTextColor(cYellow)
	} else {
		u.ipm.routeDiag.SetText("✓ Path is clean — no hop introduces persistent loss or latency.")
		u.ipm.routeDiag.SetTextColor(cGreen)
	}

	segs := pathmon.Segments(p)
	rows := make([][]string, 0, len(p.Hops))
	colors := make([]walk.Color, 0, len(p.Hops))
	for i, h := range p.Hops {
		seg := ""
		if i < len(segs) {
			seg = segs[i].String()
		}
		if !h.Responded() {
			rows = append(rows, []string{fmt.Sprint(h.TTL), "*", "", "", "", "", seg})
			colors = append(colors, cSub)
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
		rows = append(rows, []string{fmt.Sprint(h.TTL), h.Addr, trunc(who, 30), msText(h.Avg), msText(h.Max), fmt.Sprintf("%.0f%%", h.Loss*100), seg})
		col := cText
		if d.HopIndex == i {
			col = cYellow
		} else if h.Loss >= 0.5 {
			col = cRed
		}
		colors = append(colors, col)
	}
	u.ipm.route.set(rows, colors)

	var other []string
	for _, q := range s.Paths[1:] {
		other = append(other, fmt.Sprintf("%s: %d hops, AS path %s", q.Name, len(q.Hops), nz(q.ASSignature())))
	}
	if len(s.RouteChanges) > 0 {
		c := s.RouteChanges[0]
		other = append(other, fmt.Sprintf("Last route change %s to %s (hops %d→%d)", c.When.Format("Jan 2 15:04"), c.Name, c.HopsBefore, c.HopsAfter))
	}
	u.ipm.routeOther.SetText(strings.Join(other, "   ·   "))
}

func (u *ui) renderServices(s model.Snapshot) {
	if len(s.Synthetics) == 0 {
		u.ipm.servicesHead.SetText("No synthetic checks configured (see config.json).")
		u.ipm.services.set(nil, nil)
		return
	}
	up, down, pending := 0, 0, 0
	ordered := make([]synth.Summary, 0, len(s.Synthetics))
	for _, sm := range s.Synthetics {
		switch {
		case sm.Stats.Runs == 0:
			pending++
		case sm.Stats.Down():
			down++
		default:
			up++
		}
		if sm.Stats.Down() || sm.Stats.Slow() {
			ordered = append(ordered, sm)
		}
	}
	for _, sm := range s.Synthetics {
		if !(sm.Stats.Down() || sm.Stats.Slow()) {
			ordered = append(ordered, sm)
		}
	}
	head := fmt.Sprintf("%d up · %d down · %d pending", up, down, pending)
	if last := newestRun(s.Synthetics); !last.IsZero() {
		head += "   last run " + last.Format("15:04:05")
	}
	u.ipm.servicesHead.SetText(head)
	if down > 0 {
		u.ipm.servicesHead.SetTextColor(cRed)
	} else {
		u.ipm.servicesHead.SetTextColor(cSub)
	}

	rows := make([][]string, 0, len(ordered))
	colors := make([]walk.Color, 0, len(ordered))
	for _, sm := range ordered {
		st := sm.Stats
		if st.Runs == 0 {
			rows = append(rows, []string{sm.Check.Name, string(sm.Check.Category), "pending", "", "", "", "", "", ""})
			colors = append(colors, cSub)
			continue
		}
		status, col := "ok", cGreen
		switch {
		case st.Down():
			status, col = "DOWN", cRed
		case !st.Last.OK:
			status, col = "fail", cYellow
		case st.Slow():
			status, col = "slow", cYellow
		default:
			col = ratingColor(synth.RateTTFB(st.MeanTTFB))
		}
		note := st.Last.Edge
		if !st.Last.OK {
			note = st.Last.Err
			if note == "" && st.Last.Status > 0 {
				note = fmt.Sprintf("HTTP %d", st.Last.Status)
			}
		}
		rows = append(rows, []string{sm.Check.Name, string(sm.Check.Category), status,
			msText(st.MeanDNS), msText(st.MeanTLS), msText(st.MeanTTFB), msText(st.Mean),
			fmt.Sprintf("%.0f%%", st.Availability*100), trunc(note, 40)})
		colors = append(colors, col)
	}
	u.ipm.services.set(rows, colors)
}

func newestRun(sums []synth.Summary) time.Time {
	var t time.Time
	for _, s := range sums {
		if s.Stats.Last.When.After(t) {
			t = s.Stats.Last.When
		}
	}
	return t
}

func (u *ui) renderSLA(s model.Snapshot) {
	slo := u.eng.SLO()
	head := fmt.Sprintf("SLO: %.3g%% availability", slo.Availability*100)
	if slo.P95Ms > 0 {
		head += fmt.Sprintf(", internet p95 ≤ %.0f ms", slo.P95Ms)
	}
	head += " over 24 h"
	if s.AlertIncidents > 0 {
		head += fmt.Sprintf("   ·   alert compression: %d degraded ticks → %d incidents", s.AlertRaw, s.AlertIncidents)
	}
	u.ipm.slaHead.SetText(head)

	rows := make([][]string, 0, len(s.SLA))
	colors := make([]walk.Color, 0, len(s.SLA))
	for _, e := range s.SLA {
		if e.Day.Samples == 0 {
			continue
		}
		sloTxt, col := "ok", cText
		if !e.Compliance.OK {
			sloTxt, col = "BREACH", cRed
		}
		base := "—"
		if e.Baseline.Valid {
			base = fmt.Sprintf("%.0f ms", e.Baseline.Median)
		}
		now := "—"
		if e.CurrentMs > 0 {
			now = fmt.Sprintf("%.0f ms", e.CurrentMs)
			if e.Anomalous {
				now += fmt.Sprintf("  ▲ anomaly (z=%.0f)", e.Z)
				if col == cText {
					col = cYellow
				}
			}
		}
		budget := "exhausted"
		if e.Compliance.ErrorBudgetLeft >= 0 {
			budget = fmt.Sprintf("%.0f%% left", e.Compliance.ErrorBudgetLeft*100)
		}
		rows = append(rows, []string{e.Name,
			pct(e.Hour.Availability), pct(e.Day.Availability), pct(e.Week.Availability),
			msFloat(e.Day.P95), base, budget, sloTxt, now})
		colors = append(colors, col)
	}
	u.ipm.sla.set(rows, colors)
}

func (u *ui) renderIncidents(list []incident.Incident, now time.Time) {
	if len(list) == 0 {
		u.ipm.incidentsHead.SetText("No incidents recorded — connection looking clean.")
		u.ipm.incidentsHead.SetTextColor(cSub)
		u.ipm.incidents.set(nil, nil)
		return
	}
	open := 0
	for _, in := range list {
		if in.Open() {
			open++
		}
	}
	if open > 0 {
		u.ipm.incidentsHead.SetText(fmt.Sprintf("● %d incident open, %d total", open, len(list)))
		u.ipm.incidentsHead.SetTextColor(cRed)
	} else {
		u.ipm.incidentsHead.SetText(fmt.Sprintf("%d incidents, none open", len(list)))
		u.ipm.incidentsHead.SetTextColor(cSub)
	}
	rows := make([][]string, 0, len(list))
	colors := make([]walk.Color, 0, len(list))
	for _, in := range list {
		rows = append(rows, []string{fmt.Sprint(in.ID), in.Start.Format("Jan 2 15:04"), durText(in.Duration(now)),
			in.Culprit.String(), strings.ToUpper(in.Peak.String()), fmt.Sprint(in.Ticks), in.Headline})
		if in.Open() {
			colors = append(colors, cRed)
		} else {
			colors = append(colors, cText)
		}
	}
	u.ipm.incidents.set(rows, colors)
}

func (u *ui) renderBGP(s model.Snapshot) {
	if u.ipm.connPrefix == nil {
		return
	}
	if s.BGP == nil {
		u.ipm.connPrefix.SetText("…")
		u.ipm.connBGP.SetText("…")
		u.ipm.connBGP.SetTextColor(cSub)
		return
	}
	b := s.BGP
	if !b.Announced {
		u.ipm.connPrefix.SetText("not announced")
		u.ipm.connBGP.SetText("prefix not visible in BGP")
		u.ipm.connBGP.SetTextColor(cRed)
		return
	}
	u.ipm.connPrefix.SetText(fmt.Sprintf("%s via AS%d", b.Prefix, b.OriginASN))
	u.ipm.connBGP.SetText(fmt.Sprintf("seen by %.0f%% of route collectors (%d/%d)", b.Visibility*100, b.VisiblePeers, b.TotalPeers))
	if b.Healthy() {
		u.ipm.connBGP.SetTextColor(cGreen)
	} else {
		u.ipm.connBGP.SetTextColor(cYellow)
	}
}

func (u *ui) renderDNSAuth(s model.Snapshot) {
	for i := range u.ipm.dnsAuth {
		r := u.ipm.dnsAuth[i]
		if r.domain == nil {
			continue
		}
		if i >= len(s.DNSAuth) {
			r.domain.SetText("")
			r.ns.SetText("")
			r.lat.SetText("")
			r.status.SetText("")
			continue
		}
		a := s.DNSAuth[i]
		r.domain.SetText(a.Domain)
		r.ns.SetText(trunc(strings.TrimSuffix(a.NS, "."), 26))
		if !a.OK {
			r.lat.SetText("—")
			r.lat.SetTextColor(cSub)
			r.status.SetText("fail")
			r.status.SetTextColor(cRed)
			continue
		}
		r.lat.SetText(rnd(a.Latency).String())
		switch {
		case a.Latency > 250*time.Millisecond:
			r.lat.SetTextColor(cYellow)
			r.status.SetText("slow")
			r.status.SetTextColor(cYellow)
		default:
			r.lat.SetTextColor(cGreen)
			r.status.SetText("ok")
			r.status.SetTextColor(cGreen)
		}
	}
}

// --- small formatters ---

func msText(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	if d < time.Millisecond {
		return "<1 ms"
	}
	return fmt.Sprintf("%.0f ms", float64(d)/float64(time.Millisecond))
}

func msFloat(ms float64) string {
	if ms <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f ms", ms)
}

func pct(v float64) string { return fmt.Sprintf("%.2f%%", v*100) }

func durText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
