//go:build windows

// Package gui implements the native Windows interface for Agent Smith using
// lxn/walk: a dark dashboard window plus a system-tray icon. It shows the live
// verdict, a session RTT history sparkline, the concentric-ring path metrics,
// local diagnostics, and a timestamped issue log with process snapshots. Every
// value carries a mouse-over tooltip; Ctrl+mouse-wheel adjusts the font size.
// Closing the window hides it to the tray; quitting is done from the tray menu.
package gui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"github.com/lxn/walk"
	decl "github.com/lxn/walk/declarative"
	"golang.org/x/sys/windows"

	"github.com/NYBaywatch/agent-smith/internal/bufferbloat"
	"github.com/NYBaywatch/agent-smith/internal/engine"
	"github.com/NYBaywatch/agent-smith/internal/interpret"
	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/model"
)

// Dark palette (Catppuccin Mocha).
var (
	cBg     = walk.RGB(0x1e, 0x1e, 0x2e)
	cPanel  = walk.RGB(0x18, 0x18, 0x28)
	cText   = walk.RGB(0xcd, 0xd6, 0xf4)
	cSub    = walk.RGB(0x93, 0x99, 0xb2)
	cGreen  = walk.RGB(0xa6, 0xe3, 0xa1)
	cYellow = walk.RGB(0xf9, 0xe2, 0xaf)
	cRed    = walk.RGB(0xf3, 0x8b, 0xa8)
	cBlue   = walk.RGB(0x89, 0xb4, 0xfa)
	cMauve  = walk.RGB(0xcb, 0xa6, 0xf7)
	cGrid   = walk.RGB(0x31, 0x32, 0x44)
)

var (
	bgBrush    = decl.SolidColorBrush{Color: cBg}
	panelBrush = decl.SolidColorBrush{Color: cPanel}
)

const (
	maxRows   = 4      // gateway, ISP hop, 2 internet anchors
	mkControl = 0x0008 // WM_MOUSEWHEEL wParam low-word flag for the Ctrl key
)

type ringRow struct {
	ring, target, avg, p95, jitter, loss *walk.Label
}

// issueItem is one row in the events TableView (reflected by field name).
type issueItem struct {
	When     string
	Severity string
	Culprit  string
	Issue    string
}

// scaledLabel remembers a label's base font so Ctrl+wheel can rescale all text.
type scaledLabel struct {
	lbl    *walk.Label
	family string
	base   int
	bold   bool
}

type ui struct {
	mw   *walk.MainWindow
	eng  *engine.Engine
	ctx  context.Context
	tray *walk.NotifyIcon

	statusPill *walk.Label
	verdict    *walk.Label
	detail     *walk.Label
	fix        *walk.Label
	spark      *walk.CustomWidget

	issueTable  *walk.TableView
	issueDetail *walk.TextEdit
	issueItems  []*issueItem  // table model (newest first)
	issueData   []model.Issue // parallel to issueItems

	rows                  [maxRows]ringRow
	ifaceVal, wifiVal     *walk.Label
	resVal, dnsVal, bbVal *walk.Label
	bbButton              *walk.PushButton
	bbStatus              *walk.Label

	// font scaling
	fontScale  float64
	scaled     []scaledLabel
	scaleFonts []*walk.Font

	// cached GDI objects for the sparkline
	smallFont                      *walk.Font
	brushPanel                     *walk.SolidColorBrush
	penGrid, penNet, penGw, penIsp *walk.CosmeticPen

	bbRunning bool
}

// Run builds and runs the GUI, blocking until the user quits or ctx is cancelled.
func Run(ctx context.Context, e *engine.Engine) error {
	u := &ui{eng: e, ctx: ctx, fontScale: 1.0}

	mono := decl.Font{Family: "Consolas", PointSize: 10}
	hdrFont := decl.Font{Family: "Segoe UI", PointSize: 9, Bold: true}

	ringCols := []struct{ name, tip string }{
		{"Ring", "Which segment of the path this row measures: LAN gateway, the first ISP hop, then public internet anchors."},
		{"Target", "The host being pinged to represent this segment of the path."},
		{"avg", "Average round-trip time (ping). Lower is better — under 50 ms is good for real-time work, under 20 ms is excellent."},
		{"p95", "95th-percentile RTT: most pings are at or below this. Exposes spikes the average hides."},
		{"jitter", "RFC 3550 jitter — how much the ping varies. Under 5 ms is great; high jitter causes stutter, rubber-banding, and erratic throughput."},
		{"loss", "Percent of probes with no reply. Even 1–2% noticeably hurts real-time and latency-sensitive workloads."},
	}
	ringChildren := make([]decl.Widget, 0, 6*(maxRows+1))
	for _, c := range ringCols {
		ringChildren = append(ringChildren, decl.Label{Text: c.name, ToolTipText: c.tip, TextColor: cSub, Font: hdrFont, Background: panelBrush})
	}
	for i := 0; i < maxRows; i++ {
		ringChildren = append(ringChildren,
			cell(&u.rows[i].ring, cSub, hdrFont),
			cell(&u.rows[i].target, cText, mono),
			cell(&u.rows[i].avg, cText, mono),
			cell(&u.rows[i].p95, cText, mono),
			cell(&u.rows[i].jitter, cText, mono),
			cell(&u.rows[i].loss, cText, mono),
		)
	}

	err := (decl.MainWindow{
		AssignTo:   &u.mw,
		Title:      "Agent Smith — network & system performance monitor",
		Background: bgBrush,
		MinSize:    decl.Size{Width: 740, Height: 760},
		Size:       decl.Size{Width: 760, Height: 860},
		Layout:     decl.VBox{Margins: decl.Margins{Left: 12, Top: 10, Right: 12, Bottom: 10}, Spacing: 8},
		Children: []decl.Widget{
			decl.Composite{
				Background: bgBrush,
				Layout:     decl.HBox{MarginsZero: true},
				Children: []decl.Widget{
					decl.Label{Text: "🕶  AGENT SMITH", TextColor: cMauve, Font: decl.Font{Family: "Segoe UI", PointSize: 15, Bold: true}, Background: bgBrush},
					decl.HSpacer{},
					decl.Label{AssignTo: &u.statusPill, Text: "…", TextColor: cSub, Font: decl.Font{Family: "Segoe UI", PointSize: 12, Bold: true}, Background: bgBrush,
						ToolTipText: "Overall connection health, summarised from all signals below."},
				},
			},
			decl.Label{AssignTo: &u.verdict, Text: "Starting Agent Smith…", TextColor: cText, Font: decl.Font{Family: "Segoe UI", PointSize: 12, Bold: true}, Background: bgBrush,
				ToolTipText: "The headline conclusion: what, if anything, is most likely degrading your connection."},
			decl.Label{AssignTo: &u.detail, Text: "", TextColor: cSub, Background: bgBrush, MaxSize: decl.Size{Width: 720}},
			decl.Label{AssignTo: &u.fix, Text: "", TextColor: cYellow, Background: bgBrush, MaxSize: decl.Size{Width: 720},
				ToolTipText: "The recommended action to fix the detected problem."},

			decl.Label{Text: "RTT — history (persists across sessions)", TextColor: cSub, Font: hdrFont, Background: bgBrush,
				ToolTipText: "Ping over time. Blue = Internet, green = LAN/gateway, purple = ISP hop. Flat & low is best; spikes are lag. Tip: Ctrl + mouse-wheel resizes the text."},
			decl.CustomWidget{AssignTo: &u.spark, MinSize: decl.Size{Width: 320, Height: 140}, StretchFactor: 2, PaintPixels: u.paintSpark,
				ToolTipText: "Ping over time. Blue = Internet, green = LAN/gateway, purple = ISP hop."},

			decl.Label{Text: "Path — concentric rings (LAN → ISP edge → internet)", TextColor: cSub, Font: hdrFont, Background: bgBrush,
				ToolTipText: "Each ring is pinged separately. A problem that starts at one ring and persists outward is introduced there — that's how the culprit is localized."},
			decl.Composite{Background: panelBrush, Layout: decl.Grid{Columns: 6, Spacing: 6, Margins: decl.Margins{Left: 10, Top: 8, Right: 10, Bottom: 8}}, Children: ringChildren},

			decl.Label{Text: "Local", TextColor: cSub, Font: hdrFont, Background: bgBrush,
				ToolTipText: "Signals from your own PC and link that can masquerade as a network problem."},
			decl.Composite{
				Background: panelBrush,
				Layout:     decl.Grid{Columns: 2, Spacing: 6, Margins: decl.Margins{Left: 10, Top: 8, Right: 10, Bottom: 8}},
				Children: []decl.Widget{
					name("Interface", "Your active adapter, connection type (wired/Wi-Fi), negotiated link speed and MTU. A 1 Gbps NIC linking at 100 Mbps signals a cable fault."),
					cell(&u.ifaceVal, cText, mono),
					name("Wi-Fi", "Wireless signal: RSSI in dBm (closer to 0 is stronger; better than −67 dBm is good), link rate and SSID. Wi-Fi adds jitter — Ethernet is best for latency-sensitive work."),
					cell(&u.wifiVal, cText, mono),
					name("Resources", "Local CPU and memory load plus current network throughput. Sustained high CPU or a saturating transfer can feel exactly like network lag."),
					cell(&u.resVal, cText, mono),
					name("DNS", "Average time to resolve domain names. Over ~100 ms makes connecting to servers, sites, and APIs feel sluggish — try a faster resolver like 1.1.1.1."),
					cell(&u.dnsVal, cText, mono),
					name("Bufferbloat", "Latency added when the link is fully loaded, graded A+ to F. C or worse means the access-link queue is the problem (fix with router SQM/QoS)."),
					cell(&u.bbVal, cText, mono),
				},
			},

			decl.Label{Text: "Events — click one to drill into what was degraded", TextColor: cSub, Font: hdrFont, Background: bgBrush,
				ToolTipText: "Every detected problem is logged as an event. Select a row to see the exact metrics that were degraded, the system state, and a 'ps'-style snapshot of the busiest processes at that moment. Persists across sessions."},
			decl.TableView{
				AssignTo:            &u.issueTable,
				Background:          panelBrush,
				ColumnsSizable:      true,
				LastColumnStretched: true,
				MinSize:             decl.Size{Width: 320, Height: 120},
				StretchFactor:       1,
				Columns: []decl.TableViewColumn{
					{Title: "Time", DataMember: "When", Width: 80},
					{Title: "Severity", DataMember: "Severity", Width: 90},
					{Title: "Where", DataMember: "Culprit", Width: 120},
					{Title: "Issue", DataMember: "Issue", Width: 300},
				},
				OnCurrentIndexChanged: u.showIssueDetail,
				StyleCell:             u.styleIssueCell,
			},
			decl.TextEdit{AssignTo: &u.issueDetail, ReadOnly: true, Background: panelBrush, TextColor: cText, Font: mono, VScroll: true,
				MinSize: decl.Size{Width: 320, Height: 150}, StretchFactor: 1},

			decl.Composite{
				Background: bgBrush,
				Layout:     decl.HBox{MarginsZero: true},
				Children: []decl.Widget{
					decl.PushButton{AssignTo: &u.bbButton, Text: "Run Bufferbloat Test", OnClicked: u.onBufferbloat,
						ToolTipText: "Saturate your download for ~10 s and measure how much latency it adds — the metric speed tests miss."},
					decl.PushButton{Text: "Clear Events", OnClicked: u.onClearEvents,
						ToolTipText: "Remove all recorded events from the list and from disk."},
					decl.Label{AssignTo: &u.bbStatus, Text: "", TextColor: cSub, Background: bgBrush},
					decl.HSpacer{},
				},
			},
		},
	}).Create()
	if err != nil {
		return err
	}

	u.initGDI()
	defer u.disposeGDI()
	enableDarkTitleBar(uintptr(u.mw.Handle()))

	// Record base fonts for Ctrl+wheel scaling, and wire the wheel handler.
	collectLabels(u.mw, &u.scaled)
	u.mw.MouseWheel().Attach(u.onWheel)
	u.spark.MouseWheel().Attach(u.onWheel)
	u.issueDetail.MouseWheel().Attach(u.onWheel)
	u.issueTable.MouseWheel().Attach(u.onWheel)
	u.issueDetail.SetText("Select an event above to see details.")

	if err := u.setupTray(); err != nil {
		return err
	}
	defer u.tray.Dispose()

	u.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if reason == walk.CloseReasonUnknown {
			*canceled = true
			u.mw.Hide()
			u.tray.ShowInfo("Agent Smith", "Still watching your connection in the tray.")
		}
	})

	go u.consume(ctx)
	go func() {
		<-ctx.Done()
		u.mw.Synchronize(func() { walk.App().Exit(0) })
	}()

	u.render(e.Latest())
	u.mw.Run()
	return nil
}

func cell(assign **walk.Label, color walk.Color, font decl.Font) decl.Label {
	return decl.Label{AssignTo: assign, Text: "…", TextColor: color, Font: font, Background: panelBrush}
}

func name(text, tip string) decl.Label {
	return decl.Label{Text: text, ToolTipText: tip, TextColor: cSub, Font: decl.Font{Family: "Segoe UI", PointSize: 9, Bold: true}, Background: panelBrush}
}

// --- dark title bar (DWM) ---

var (
	dwmapi         = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetAttr = dwmapi.NewProc("DwmSetWindowAttribute")
)

func enableDarkTitleBar(hwnd uintptr) {
	if hwnd == 0 || procDwmSetAttr.Find() != nil {
		return
	}
	var on int32 = 1
	// DWMWA_USE_IMMERSIVE_DARK_MODE: 20 on Win10 20H1+/Win11, 19 on earlier builds.
	for _, attr := range []uintptr{20, 19} {
		if r, _, _ := procDwmSetAttr.Call(hwnd, attr, uintptr(unsafe.Pointer(&on)), unsafe.Sizeof(on)); r == 0 {
			return
		}
	}
}

// --- font scaling ---

func collectLabels(c walk.Container, out *[]scaledLabel) {
	ch := c.Children()
	if ch == nil {
		return
	}
	for i := 0; i < ch.Len(); i++ {
		w := ch.At(i)
		if lbl, ok := w.(*walk.Label); ok {
			if f := lbl.Font(); f != nil {
				*out = append(*out, scaledLabel{lbl: lbl, family: f.Family(), base: f.PointSize(), bold: f.Style()&walk.FontBold != 0})
			}
		}
		if cont, ok := w.(walk.Container); ok {
			collectLabels(cont, out)
		}
	}
}

func (u *ui) onWheel(x, y int, button walk.MouseButton) {
	w := uint32(button)
	if w&mkControl == 0 {
		return // only Ctrl+wheel adjusts the font
	}
	delta := int16(w >> 16)
	if delta == 0 {
		return
	}
	if delta > 0 {
		u.fontScale *= 1.1
	} else {
		u.fontScale /= 1.1
	}
	if u.fontScale < 0.7 {
		u.fontScale = 0.7
	}
	if u.fontScale > 2.2 {
		u.fontScale = 2.2
	}
	u.applyFontScale()
}

func (u *ui) applyFontScale() {
	cache := map[string]*walk.Font{}
	for _, s := range u.scaled {
		size := int(float64(s.base)*u.fontScale + 0.5)
		if size < 6 {
			size = 6
		}
		if size > 48 {
			size = 48
		}
		var style walk.FontStyle
		if s.bold {
			style = walk.FontBold
		}
		key := fmt.Sprintf("%s|%d|%d", s.family, size, style)
		f := cache[key]
		if f == nil {
			nf, err := walk.NewFont(s.family, size, style)
			if err != nil {
				continue
			}
			f = nf
			cache[key] = f
			u.scaleFonts = append(u.scaleFonts, f)
		}
		s.lbl.SetFont(f)
	}
}

func (u *ui) initGDI() {
	u.smallFont, _ = walk.NewFont("Segoe UI", 8, 0)
	u.brushPanel, _ = walk.NewSolidColorBrush(cPanel)
	u.penGrid, _ = walk.NewCosmeticPen(walk.PenSolid, cGrid)
	u.penNet, _ = walk.NewCosmeticPen(walk.PenSolid, cBlue)
	u.penGw, _ = walk.NewCosmeticPen(walk.PenSolid, cGreen)
	u.penIsp, _ = walk.NewCosmeticPen(walk.PenSolid, cMauve)
}

func (u *ui) disposeGDI() {
	objs := []interface{ Dispose() }{u.smallFont, u.brushPanel, u.penGrid, u.penNet, u.penGw, u.penIsp}
	for _, f := range u.scaleFonts {
		objs = append(objs, f)
	}
	for _, d := range objs {
		if d != nil {
			d.Dispose()
		}
	}
}

func (u *ui) setupTray() error {
	ni, err := walk.NewNotifyIcon(u.mw)
	if err != nil {
		return err
	}
	u.tray = ni
	ni.SetToolTip("Agent Smith")
	if ic := walk.IconApplication(); ic != nil {
		ni.SetIcon(ic)
	}
	ni.SetVisible(true)

	add := func(text string, fn func()) {
		a := walk.NewAction()
		a.SetText(text)
		a.Triggered().Attach(fn)
		ni.ContextMenu().Actions().Add(a)
	}
	add("&Show dashboard", u.showWindow)
	add("Run &bufferbloat test", func() { u.showWindow(); u.onBufferbloat() })
	ni.ContextMenu().Actions().Add(walk.NewSeparatorAction())
	add("&Quit", func() { walk.App().Exit(0) })

	ni.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			u.showWindow()
		}
	})
	return nil
}

func (u *ui) showWindow() {
	u.mw.Show()
	u.mw.Activate()
}

func (u *ui) consume(ctx context.Context) {
	ch := u.eng.Subscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case snap := <-ch:
			s := snap
			u.mw.Synchronize(func() { u.render(s) })
		}
	}
}

func (u *ui) onBufferbloat() {
	if u.bbRunning {
		return
	}
	u.bbRunning = true
	u.bbButton.SetEnabled(false)
	u.bbStatus.SetText("Testing… saturating link (~10s)")
	go func() {
		ctx, cancel := context.WithTimeout(u.ctx, 40*time.Second)
		defer cancel()
		res, err := u.eng.RunBufferbloat(ctx, bufferbloat.DefaultOptions())
		u.mw.Synchronize(func() {
			u.bbRunning = false
			u.bbButton.SetEnabled(true)
			if err != nil {
				u.bbStatus.SetText("Test failed: " + err.Error())
				return
			}
			u.bbStatus.SetText(fmt.Sprintf("Grade %s  (+%v under load, %.0f Mbps down)",
				res.Grade, res.Added.Round(time.Millisecond), res.DownloadMbps))
		})
	}()
}

func (u *ui) render(s model.Snapshot) {
	v := s.Verdict
	u.statusPill.SetText(badge(v.Severity))
	u.statusPill.SetTextColor(severityColor(v.Severity))
	u.verdict.SetText(v.Headline)
	u.detail.SetText(v.Detail)
	if v.Fix != "" {
		u.fix.SetText("Fix: " + v.Fix)
	} else {
		u.fix.SetText("")
	}

	type rr struct {
		ring string
		ts   *model.TargetStats
	}
	list := []rr{{"LAN", s.Gateway}, {"ISP", s.ISPHop}}
	for i := range s.Internet {
		label := "NET"
		if i > 0 {
			label = ""
		}
		list = append(list, rr{label, &s.Internet[i]})
	}
	for i := 0; i < maxRows; i++ {
		switch {
		case i < len(list) && list[i].ts != nil:
			u.fillRow(u.rows[i], list[i].ring, list[i].ts)
		case i < len(list):
			u.blankRow(u.rows[i], list[i].ring, "(discovering…)")
		default:
			u.blankRow(u.rows[i], "", "")
		}
	}

	u.spark.Invalidate()

	if s.Net != nil && s.Net.Active != nil {
		a := s.Net.Active
		txt := fmt.Sprintf("%s · %s · %d Mbps · MTU %d", a.Name, a.Media, a.LinkMbps, a.MTU)
		if errs := a.InErrors + a.OutErrors + a.InDiscards + a.OutDiscards; errs > 0 {
			u.ifaceVal.SetText(txt + fmt.Sprintf("  ⚠ %d errors", errs))
			u.ifaceVal.SetTextColor(cYellow)
		} else {
			u.ifaceVal.SetText(txt)
			u.ifaceVal.SetTextColor(cText)
		}
	} else {
		u.ifaceVal.SetText("—")
	}
	if s.Net != nil && s.Net.WiFi != nil {
		w := s.Net.WiFi
		u.wifiVal.SetText(fmt.Sprintf("%q · %d dBm (%d%%) · rx %.0f / tx %.0f Mbps", w.SSID, w.RSSI, w.SignalQuality, w.RxMbps, w.TxMbps))
		u.wifiVal.SetTextColor(rssiColor(w.RSSI))
	} else {
		u.wifiVal.SetText("— (wired connection)")
		u.wifiVal.SetTextColor(cSub)
	}
	u.resVal.SetText(fmt.Sprintf("CPU %.0f%% · Mem %.0f%% (%.1f/%.1f GB) · GPU %s · net ↓%.1f / ↑%.1f Mbps",
		s.Sys.CPUPercent, s.Sys.MemPercent, s.Sys.MemUsedGB, s.Sys.MemTotalGB, gpuStr(s.Sys.GPUPercent), s.Sys.InMbps, s.Sys.OutMbps))
	u.resVal.SetTextColor(cText)
	if s.DNS.Lookups > 0 {
		u.dnsVal.SetText(fmt.Sprintf("%v avg", rnd(s.DNS.Avg)))
		if s.DNS.Slow() {
			u.dnsVal.SetTextColor(cYellow)
		} else {
			u.dnsVal.SetTextColor(cText)
		}
	} else {
		u.dnsVal.SetText("…")
	}
	if s.Bufferbloat != nil {
		bb := s.Bufferbloat
		u.bbVal.SetText(fmt.Sprintf("grade %s · +%v under load · %.0f Mbps down", bb.Grade, bb.Added.Round(time.Millisecond), bb.DownloadMbps))
		u.bbVal.SetTextColor(gradeColor(bb.Grade))
	} else {
		u.bbVal.SetText("not measured — click \"Run Bufferbloat Test\"")
		u.bbVal.SetTextColor(cSub)
	}

	// Event list — only rebuild when the count changes.
	issues := u.eng.Issues()
	if len(issues) != len(u.issueData) {
		u.rebuildIssues(issues)
	}

	if v.Culprit != model.CulpritHealthy {
		u.tray.SetToolTip(fmt.Sprintf("Agent Smith — %s: %s", v.Severity, v.Culprit))
	} else {
		u.tray.SetToolTip("Agent Smith — connection healthy")
	}
}

// rebuildIssues refreshes the event TableView from the engine's issue log
// (newest first) and keeps a parallel slice for detail lookup + cell styling.
func (u *ui) rebuildIssues(issues []model.Issue) {
	data := make([]model.Issue, 0, len(issues))
	items := make([]*issueItem, 0, len(issues))
	for i := len(issues) - 1; i >= 0; i-- { // newest first
		is := issues[i]
		data = append(data, is)
		items = append(items, &issueItem{
			When:     is.Time.Format("15:04:05"),
			Severity: strings.ToUpper(is.Severity.String()),
			Culprit:  is.Culprit.String(),
			Issue:    is.Headline,
		})
	}
	prev := u.issueTable.CurrentIndex()
	u.issueData = data
	u.issueItems = items
	_ = u.issueTable.SetModel(items)
	if len(items) == 0 {
		u.issueDetail.SetText("No issues recorded yet — connection looking clean.")
		return
	}
	idx := 0
	if prev >= 0 && prev < len(items) {
		idx = prev
	}
	_ = u.issueTable.SetCurrentIndex(idx)
	u.showIssueDetail()
}

func (u *ui) onClearEvents() {
	u.eng.ClearIssues()
	u.rebuildIssues(u.eng.Issues())
	u.issueDetail.SetText("Select an event above to see details.")
}

func (u *ui) showIssueDetail() {
	idx := u.issueTable.CurrentIndex()
	if idx < 0 || idx >= len(u.issueData) {
		return
	}
	u.issueDetail.SetText(formatIssueDetail(u.issueData[idx]))
}

// styleIssueCell paints event rows on the dark panel and tints text by severity.
func (u *ui) styleIssueCell(style *walk.CellStyle) {
	style.BackgroundColor = cPanel
	row := style.Row()
	if row >= 0 && row < len(u.issueData) {
		style.TextColor = severityColor(u.issueData[row].Severity)
	} else {
		style.TextColor = cText
	}
}

func formatIssueDetail(is model.Issue) string {
	const nl = "\r\n"
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s", is.Headline, nl)
	fmt.Fprintf(&b, "%s  ·  %s  ·  %s%s", is.Time.Format("2006-01-02 15:04:05"), strings.ToUpper(is.Severity.String()), is.Culprit, nl)

	// Plain-language meaning first — what this actually means for the user.
	fmt.Fprintf(&b, "%sWHAT THIS MEANS%s%s%s", nl, nl, wordWrap(interpret.Summary(is), 78, "  "), nl)

	// Interpreted measurements: value + rating + impact, aligned.
	fmt.Fprintf(&b, "%sMEASUREMENTS%s", nl, nl)
	for _, l := range interpret.Lines(is.Metrics) {
		fmt.Fprintf(&b, "  %-14s %-18s %-10s %s%s", l.Label, l.Value, l.Rating, l.Meaning, nl)
	}

	if is.Fix != "" {
		fmt.Fprintf(&b, "%sSUGGESTED FIX%s%s%s", nl, nl, wordWrap(is.Fix, 78, "  "), nl)
	}

	if len(is.Procs) > 0 {
		fmt.Fprintf(&b, "%sTOP PROCESSES (ps snapshot)%s", nl, nl)
		for _, p := range is.Procs {
			fmt.Fprintf(&b, "  %-26s %5.0f%% CPU  %7.0f MB%s", trunc(p.Name, 26), p.CPU, p.MemMB, nl)
		}
	}
	return b.String()
}

// wordWrap wraps text to width columns, prefixing each line with indent.
func wordWrap(s string, width int, indent string) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	line := indent + words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width {
			b.WriteString(line + "\r\n")
			line = indent + w
		} else {
			line += " " + w
		}
	}
	b.WriteString(line)
	return b.String()
}

func (u *ui) fillRow(r ringRow, ring string, ts *model.TargetStats) {
	r.ring.SetText(ring)
	r.target.SetText(trunc(ts.Name+" "+ts.Host, 26))
	if !ts.Alive {
		r.avg.SetText("no reply")
		r.avg.SetTextColor(cRed)
		for _, l := range []*walk.Label{r.p95, r.jitter, r.loss} {
			l.SetText("—")
			l.SetTextColor(cSub)
		}
		return
	}
	st := ts.Stats
	r.avg.SetText(rnd(st.Mean).String())
	r.avg.SetTextColor(ratingColor(metrics.RateLatency(st.Mean)))
	r.p95.SetText(rnd(st.P95).String())
	r.p95.SetTextColor(cText)
	r.jitter.SetText(rnd(st.Jitter).String())
	r.jitter.SetTextColor(ratingColor(metrics.RateJitter(st.Jitter)))
	r.loss.SetText(fmt.Sprintf("%.1f%%", st.Loss*100))
	r.loss.SetTextColor(lossColor(st.Loss))
}

func (u *ui) blankRow(r ringRow, ring, target string) {
	r.ring.SetText(ring)
	r.target.SetText(target)
	for _, l := range []*walk.Label{r.avg, r.p95, r.jitter, r.loss} {
		l.SetText("")
	}
}

// paintSpark draws the persisted RTT history (read from the engine).
func (u *ui) paintSpark(canvas *walk.Canvas, _ walk.Rectangle) error {
	if u.brushPanel == nil {
		return nil
	}
	cb := u.spark.ClientBoundsPixels()
	W, H := cb.Width, cb.Height
	canvas.FillRectanglePixels(u.brushPanel, walk.Rectangle{X: 0, Y: 0, Width: W, Height: H})

	hist := u.eng.History()
	if len(hist) > 1200 {
		hist = hist[len(hist)-1200:]
	}
	extract := func(sel func(model.HistPoint) float64) []float64 {
		out := make([]float64, len(hist))
		last := 0.0
		for i, hp := range hist {
			if v := sel(hp); v > 0 {
				last = v
			}
			out[i] = last
		}
		return out
	}
	gw := extract(func(h model.HistPoint) float64 { return h.GwMs })
	isp := extract(func(h model.HistPoint) float64 { return h.IspMs })
	net := extract(func(h model.HistPoint) float64 { return h.NetMs })

	max := 20.0
	for _, s := range [][]float64{gw, isp, net} {
		for _, v := range s {
			if v > max {
				max = v
			}
		}
	}
	max *= 1.15

	const pad, legendH = 6, 16
	plotW, plotH := W-2*pad, H-2*pad-legendH
	if plotW < 4 || plotH < 4 {
		return nil
	}

	for _, f := range []float64{0, 0.5, 1} {
		y := pad + int(float64(plotH)*f)
		canvas.DrawLinePixels(u.penGrid, walk.Point{X: pad, Y: y}, walk.Point{X: pad + plotW, Y: y})
		if u.smallFont != nil {
			canvas.DrawTextPixels(fmt.Sprintf("%.0f ms", max*(1-f)), u.smallFont, cSub,
				walk.Rectangle{X: pad + plotW - 48, Y: y, Width: 48, Height: 12}, walk.TextRight|walk.TextSingleLine)
		}
	}

	draw := func(s []float64, pen *walk.CosmeticPen) {
		n := len(s)
		if n < 2 || pen == nil {
			return
		}
		pts := make([]walk.Point, n)
		for i, v := range s {
			x := pad + int(float64(i)*float64(plotW)/float64(n-1))
			y := pad + plotH - int(v/max*float64(plotH))
			if y < pad {
				y = pad
			}
			if y > pad+plotH {
				y = pad + plotH
			}
			pts[i] = walk.Point{X: x, Y: y}
		}
		canvas.DrawPolylinePixels(pen, pts)
	}
	draw(gw, u.penGw)
	draw(isp, u.penIsp)
	draw(net, u.penNet)

	if u.smallFont != nil {
		y := H - legendH + 2
		canvas.DrawTextPixels("● Internet", u.smallFont, cBlue, walk.Rectangle{X: pad, Y: y, Width: 90, Height: 12}, walk.TextLeft|walk.TextSingleLine)
		canvas.DrawTextPixels("● LAN", u.smallFont, cGreen, walk.Rectangle{X: pad + 96, Y: y, Width: 60, Height: 12}, walk.TextLeft|walk.TextSingleLine)
		canvas.DrawTextPixels("● ISP hop", u.smallFont, cMauve, walk.Rectangle{X: pad + 160, Y: y, Width: 90, Height: 12}, walk.TextLeft|walk.TextSingleLine)
	}
	return nil
}

func badge(s model.Severity) string {
	switch s {
	case model.SevOK:
		return "● OK"
	case model.SevWatch:
		return "● WATCH"
	case model.SevDegraded:
		return "● DEGRADED"
	case model.SevCritical:
		return "● CRITICAL"
	default:
		return "●"
	}
}

func severityColor(s model.Severity) walk.Color {
	switch s {
	case model.SevOK:
		return cGreen
	case model.SevWatch:
		return cYellow
	default:
		return cRed
	}
}

func ratingColor(r metrics.Rating) walk.Color {
	switch r {
	case metrics.RatingExcellent, metrics.RatingGood:
		return cGreen
	case metrics.RatingPlayable:
		return cYellow
	case metrics.RatingPoor:
		return cRed
	default:
		return cSub
	}
}

func lossColor(loss float64) walk.Color {
	switch {
	case loss > 0.025:
		return cRed
	case loss > 0.01:
		return cYellow
	default:
		return cGreen
	}
}

func rssiColor(dBm int) walk.Color {
	switch {
	case dBm >= -60:
		return cGreen
	case dBm >= -70:
		return cYellow
	default:
		return cRed
	}
}

func gradeColor(grade string) walk.Color {
	switch grade {
	case "A+", "A", "B":
		return cGreen
	case "C":
		return cYellow
	default:
		return cRed
	}
}

func rnd(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d >= time.Millisecond {
		return d.Round(100 * time.Microsecond)
	}
	return d.Round(10 * time.Microsecond)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// gpuStr formats a GPU percentage, showing "n/a" when unavailable (-1).
func gpuStr(pct float64) string {
	if pct < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", pct)
}
