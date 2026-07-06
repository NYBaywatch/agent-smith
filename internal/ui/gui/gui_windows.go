//go:build windows

// Package gui implements the native Windows interface for Agent Smith using
// lxn/walk: an ops-style dark dashboard (stat tiles, an area RTT chart, a path
// table, painted system bars, and an event log with drill-down) plus a
// system-tray icon. Every value carries a mouse-over tooltip; Ctrl+mouse-wheel
// adjusts the font size. Closing the window hides it to the tray.
package gui

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"image/png"
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

// Ops/Grafana-style dark palette ("Refined Dark").
var (
	cBg     = walk.RGB(0x0d, 0x0f, 0x12)
	cPanel  = walk.RGB(0x16, 0x1a, 0x20)
	cPanel2 = walk.RGB(0x1b, 0x1e, 0x24) // system-bar track (unchanged)
	cHdr    = walk.RGB(0x12, 0x15, 0x1a) // tab strip / header bar
	cLine   = walk.RGB(0x27, 0x2c, 0x34)
	cText   = walk.RGB(0xe6, 0xe8, 0xeb)
	cSub    = walk.RGB(0x8b, 0x92, 0x9c)
	cNet    = walk.RGB(0x4f, 0x8c, 0xff)
	cLan    = walk.RGB(0x34, 0xd3, 0x99)
	cIsp    = walk.RGB(0xc0, 0x84, 0xfc)
	cGreen  = walk.RGB(0x34, 0xd3, 0x99)
	cYellow = walk.RGB(0xfb, 0xbf, 0x24)
	cRed    = walk.RGB(0xf8, 0x71, 0x71)
	cAccent = walk.RGB(0x4f, 0x8c, 0xff)
	cArea   = walk.RGB(0x1e, 0x34, 0x5c)
)

var (
	bgBrush    = decl.SolidColorBrush{Color: cBg}
	panelBrush = decl.SolidColorBrush{Color: cPanel}
)

//go:embed app.png
var appPNG []byte

// loadAppIcon decodes the embedded Matrix-themed icon for the window and tray.
func loadAppIcon() *walk.Icon {
	im, err := png.Decode(bytes.NewReader(appPNG))
	if err != nil {
		return nil
	}
	ic, err := walk.NewIconFromImage(im)
	if err != nil {
		return nil
	}
	return ic
}

const (
	maxRows   = 4
	mkControl = 0x0008 // WM_MOUSEWHEEL Ctrl flag
)

func mg(l, t, r, b int) decl.Margins { return decl.Margins{Left: l, Top: t, Right: r, Bottom: b} }

type ringRow struct {
	ring, target, avg, p95, jitter, loss *walk.Label
}

type statTile struct{ val, sub *walk.Label }

// issueItem is one row in the events TableView.
type issueItem struct {
	When, Severity, Culprit, Issue string
}

// issueModel is an explicit (non-sorting) TableView model so the displayed row
// order stays aligned with issueData for selection and cell styling.
type issueModel struct {
	walk.TableModelBase
	items []*issueItem
}

func (m *issueModel) RowCount() int { return len(m.items) }

func (m *issueModel) Value(row, col int) interface{} {
	it := m.items[row]
	switch col {
	case 0:
		return it.When
	case 1:
		return it.Severity
	case 2:
		return it.Culprit
	default:
		return it.Issue
	}
}

type scaledLabel struct {
	lbl    *walk.Label
	family string
	base   int
	bold   bool
}

type ui struct {
	mw      *walk.MainWindow
	eng     *engine.Engine
	ctx     context.Context
	tray    *walk.NotifyIcon
	appIcon *walk.Icon

	// custom tab strip (path, connection, system, dns, events)
	tabHdrW    [5]*walk.CustomWidget
	tabContent [5]*walk.Composite
	tabTitle   [5]string
	activeTab  int

	// connection panel
	connISP, connOrg, connIP, connASN, connLoc, connType, connRDNS *walk.Label
	connSupport, connOutage                                        *walk.Label
	// dns per-resolver rows
	dnsRows [4]struct{ name, addr, avg, status *walk.Label }

	statusPill *walk.Label
	verdict    *walk.Label
	spark      *walk.CustomWidget
	sysBars    *walk.CustomWidget

	tiles [4]statTile // latency, jitter, loss, bufferbloat

	rows [maxRows]ringRow

	issueTable  *walk.TableView
	issueDetail *walk.TextEdit
	issueItems  []*issueItem
	issueData   []model.Issue

	bbButton  *walk.PushButton
	bbStatus  *walk.Label
	bbRunning bool

	// live system values for the painted bars
	sCPU, sMem, sMemUsed, sMemTot, sGPU, sIn, sOut float64

	// font scaling
	fontScale  float64
	scaled     []scaledLabel
	scaleFonts []*walk.Font

	// cached GDI objects
	smallFont, monoFont, hdrFont      *walk.Font
	brushPanel, brushTrack, brushBg   *walk.SolidColorBrush
	brushGreen, brushYellow, brushRed *walk.SolidColorBrush
	brushAccent, brushHdr             *walk.SolidColorBrush
	penGrid, penNet, penGw, penIsp    *walk.CosmeticPen
}

// Run builds and runs the GUI.
func Run(ctx context.Context, e *engine.Engine) error {
	u := &ui{eng: e, ctx: ctx, fontScale: 1.0}
	u.appIcon = loadAppIcon()

	mono := decl.Font{Family: "Consolas", PointSize: 9}
	hdr := decl.Font{Family: "Segoe UI", PointSize: 8, Bold: true}
	tileV := decl.Font{Family: "Consolas", PointSize: 19, Bold: true}
	tileS := decl.Font{Family: "Segoe UI", PointSize: 8}

	// Stat tiles row.
	tileDefs := []struct {
		title, tip string
	}{
		{"LATENCY", "Average round-trip time to the internet. Lower is better — under 50 ms is good, under 20 ms excellent."},
		{"JITTER", "How much ping varies (RFC 3550). Under 5 ms is great; high jitter causes stutter and uneven throughput."},
		{"PACKET LOSS", "Percent of probes with no reply. Even 1–2% hurts real-time and latency-sensitive workloads."},
		{"BUFFERBLOAT", "Latency added when the link is saturated, graded A+…F. Run the test to measure."},
	}
	tileWidgets := make([]decl.Widget, 4)
	for i, td := range tileDefs {
		tileWidgets[i] = decl.Composite{
			Background:  panelBrush,
			ToolTipText: td.tip,
			Layout:      decl.VBox{Margins: mg(12, 10, 12, 10), Spacing: 4},
			Children: []decl.Widget{
				decl.Label{Text: td.title, TextColor: cSub, Font: hdr},
				decl.Label{AssignTo: &u.tiles[i].val, Text: "—", TextColor: cText, Font: tileV},
				decl.Label{AssignTo: &u.tiles[i].sub, Text: "", TextColor: cSub, Font: tileS},
			},
		}
	}

	// Path rings grid.
	ringCols := []struct{ name, tip string }{
		{"Ring", "Which path segment this row measures."},
		{"Target", "Host pinged for this segment."},
		{"avg", "Average RTT."},
		{"p95", "95th-percentile RTT — exposes spikes the average hides."},
		{"jitter", "RFC 3550 jitter — timing variation."},
		{"loss", "Percent of probes with no reply."},
	}
	ringChildren := make([]decl.Widget, 0, 6*(maxRows+1))
	for _, c := range ringCols {
		ringChildren = append(ringChildren, decl.Label{Text: c.name, ToolTipText: c.tip, TextColor: cSub, Font: hdr, Background: panelBrush})
	}
	for i := 0; i < maxRows; i++ {
		ringChildren = append(ringChildren,
			cell(&u.rows[i].ring, cSub, hdr),
			cell(&u.rows[i].target, cText, mono),
			cell(&u.rows[i].avg, cText, mono),
			cell(&u.rows[i].p95, cText, mono),
			cell(&u.rows[i].jitter, cText, mono),
			cell(&u.rows[i].loss, cText, mono),
		)
	}

	// Connection panel (ISP / public IP / ASN).
	kFont := decl.Font{Family: "Segoe UI", PointSize: 9}
	kname := func(s string) decl.Label {
		return decl.Label{Text: s, TextColor: cSub, Font: kFont, Background: panelBrush}
	}
	connChildren := []decl.Widget{
		kname("ISP"), cell(&u.connISP, cText, mono),
		kname("Plan / org"), cell(&u.connOrg, cText, mono),
		kname("Public IP"), cell(&u.connIP, cText, mono),
		kname("Network (ASN)"), cell(&u.connASN, cText, mono),
		kname("Location"), cell(&u.connLoc, cText, mono),
		kname("Type"), cell(&u.connType, cText, mono),
		kname("Reverse DNS"), cell(&u.connRDNS, cText, mono),
		kname("Support ☎"), cell(&u.connSupport, cGreen, decl.Font{Family: "Consolas", PointSize: 11, Bold: true}),
		kname("Outage page"), cell(&u.connOutage, cSub, mono),
	}

	// DNS resolver comparison grid.
	dnsHdr := func(s string) decl.Label {
		return decl.Label{Text: s, TextColor: cSub, Font: hdr, Background: panelBrush}
	}
	dnsChildren := []decl.Widget{dnsHdr("Resolver"), dnsHdr("Address"), dnsHdr("Avg"), dnsHdr("Status")}
	for i := 0; i < len(u.dnsRows); i++ {
		dnsChildren = append(dnsChildren,
			cell(&u.dnsRows[i].name, cText, mono),
			cell(&u.dnsRows[i].addr, cSub, mono),
			cell(&u.dnsRows[i].avg, cText, mono),
			cell(&u.dnsRows[i].status, cText, mono),
		)
	}

	// Custom dark tab strip driving the five content panels.
	tabDefs := []struct{ title, tip string }{
		{"PATH", "LAN gateway → ISP edge → internet, each with avg/p95/jitter/loss."},
		{"CONNECTION", "Your public identity: ISP, public IP, ASN, location."},
		{"SYSTEM", "Local CPU, memory and GPU load plus throughput."},
		{"DNS", "Resolution latency per resolver — yours vs public."},
		{"EVENTS", "Detected problems; click a row to drill into cause + processes."},
	}
	tabStrip := make([]decl.Widget, 0, len(tabDefs)+1)
	for i, td := range tabDefs {
		i := i
		u.tabTitle[i] = td.title
		tabStrip = append(tabStrip, decl.CustomWidget{
			AssignTo:    &u.tabHdrW[i],
			MinSize:     decl.Size{Width: 96, Height: 30},
			ToolTipText: td.tip,
			PaintPixels: func(c *walk.Canvas, _ walk.Rectangle) error { return u.paintTab(i, c) },
			OnMouseDown: func(x, y int, b walk.MouseButton) { u.selectTab(i) },
		})
	}
	tabStrip = append(tabStrip, decl.HSpacer{})

	err := (decl.MainWindow{
		AssignTo:   &u.mw,
		Title:      "Agent Smith — network & system performance monitor",
		Icon:       u.appIcon,
		Background: bgBrush,
		MinSize:    decl.Size{Width: 760, Height: 560},
		Size:       decl.Size{Width: 780, Height: 980},
		Layout:     decl.VBox{Margins: mg(14, 12, 14, 12), Spacing: 8},
		Children: []decl.Widget{
			// Header (always visible).
			decl.Composite{
				Background: bgBrush,
				Layout:     decl.HBox{MarginsZero: true, Spacing: 8},
				Children: []decl.Widget{
					decl.Label{Text: "◈ AGENT SMITH", NoPrefix: true, TextColor: cIsp, Font: decl.Font{Family: "Segoe UI", PointSize: 14, Bold: true}, Background: bgBrush, MinSize: decl.Size{Width: 185}},
					decl.Label{Text: "network · system performance", NoPrefix: true, TextColor: cSub, Font: decl.Font{Family: "Segoe UI", PointSize: 9}, Background: bgBrush},
					decl.HSpacer{},
					decl.Label{AssignTo: &u.statusPill, Text: "● STARTING", NoPrefix: true, TextColor: cSub, Font: decl.Font{Family: "Segoe UI", PointSize: 10, Bold: true}, Background: bgBrush,
						ToolTipText: "Overall status, summarised from all signals."},
				},
			},
			decl.Label{AssignTo: &u.verdict, Text: "Starting Agent Smith…", TextColor: cSub, Background: bgBrush, MaxSize: decl.Size{Width: 740},
				ToolTipText: "Current conclusion and recommended fix, if any."},

			// Stat tiles (always visible).
			decl.Composite{Background: bgBrush, Layout: decl.Grid{Columns: 4, Spacing: 10, MarginsZero: true}, Children: tileWidgets},

			// RTT chart — pinned, fixed height.
			decl.Composite{Background: panelBrush, Layout: decl.VBox{MarginsZero: true}, MinSize: decl.Size{Width: 320, Height: 150}, Children: []decl.Widget{
				decl.CustomWidget{AssignTo: &u.spark, MinSize: decl.Size{Width: 320, Height: 140}, PaintPixels: u.paintSpark,
					ToolTipText: "RTT over time. Blue = Internet, green = LAN, purple = ISP hop. Ctrl+wheel resizes text."},
			}},

			// Tab strip.
			decl.Composite{Background: bgBrush, Layout: decl.HBox{MarginsZero: true, Spacing: 2}, Children: tabStrip},

			// Tab 0: Path.
			decl.Composite{AssignTo: &u.tabContent[0], Background: panelBrush, StretchFactor: 2, Layout: decl.Grid{Columns: 6, Spacing: 5, Margins: mg(14, 10, 14, 10)}, Children: ringChildren},

			// Tab 1: Connection.
			decl.Composite{AssignTo: &u.tabContent[1], Visible: false, Background: panelBrush, StretchFactor: 2, Layout: decl.Grid{Columns: 2, Spacing: 5, Margins: mg(14, 10, 14, 10)}, Children: connChildren},

			// Tab 2: System.
			decl.Composite{AssignTo: &u.tabContent[2], Visible: false, Background: panelBrush, StretchFactor: 2, Layout: decl.VBox{Margins: mg(12, 10, 12, 10)}, Children: []decl.Widget{
				decl.CustomWidget{AssignTo: &u.sysBars, MinSize: decl.Size{Width: 200, Height: 132}, PaintPixels: u.paintSysBars},
			}},

			// Tab 3: DNS.
			decl.Composite{AssignTo: &u.tabContent[3], Visible: false, Background: panelBrush, StretchFactor: 2, Layout: decl.Grid{Columns: 4, Spacing: 5, Margins: mg(14, 10, 14, 10)}, Children: dnsChildren},

			// Tab 4: Events.
			decl.Composite{AssignTo: &u.tabContent[4], Visible: false, Background: panelBrush, StretchFactor: 2, Layout: decl.VBox{MarginsZero: true, Spacing: 0}, Children: []decl.Widget{
				decl.TableView{
					AssignTo: &u.issueTable, Background: panelBrush, ColumnsSizable: true, LastColumnStretched: true,
					MinSize: decl.Size{Width: 320, Height: 110}, StretchFactor: 1,
					Columns: []decl.TableViewColumn{
						{Title: "Time", Width: 80},
						{Title: "Severity", Width: 90},
						{Title: "Where", Width: 120},
						{Title: "Issue", Width: 300},
					},
					OnCurrentIndexChanged: u.showIssueDetail,
					StyleCell:             u.styleIssueCell,
				},
				decl.TextEdit{AssignTo: &u.issueDetail, ReadOnly: true, Background: panelBrush, TextColor: cText, Font: mono, VScroll: true,
					MinSize: decl.Size{Width: 320, Height: 150}, StretchFactor: 1},
				decl.Composite{Background: panelBrush, Layout: decl.HBox{Margins: mg(0, 6, 0, 0), Spacing: 8}, Children: []decl.Widget{
					decl.HSpacer{},
					decl.PushButton{Text: "Clear Events", OnClicked: u.onClearEvents, ToolTipText: "Remove all recorded events (list and disk)."},
				}},
			}},

			// Actions (always visible).
			decl.Composite{Background: bgBrush, Layout: decl.HBox{MarginsZero: true, Spacing: 8}, Children: []decl.Widget{
				decl.PushButton{AssignTo: &u.bbButton, Text: "Run Bufferbloat Test", OnClicked: u.onBufferbloat,
					ToolTipText: "Saturate your download for ~10 s and grade the added latency."},
				decl.Label{AssignTo: &u.bbStatus, Text: "", TextColor: cSub, Background: bgBrush},
				decl.HSpacer{},
			}},
		},
	}).Create()
	if err != nil {
		return err
	}

	u.initGDI()
	defer u.disposeGDI()
	enableDarkTitleBar(uintptr(u.mw.Handle()))

	u.selectTab(0)

	collectLabels(u.mw, &u.scaled)
	u.mw.MouseWheel().Attach(u.onWheel)
	u.spark.MouseWheel().Attach(u.onWheel)
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
	go func() { <-ctx.Done(); u.mw.Synchronize(func() { walk.App().Exit(0) }) }()

	u.render(e.Latest())
	u.mw.Run()
	return nil
}

func cell(assign **walk.Label, color walk.Color, font decl.Font) decl.Label {
	return decl.Label{AssignTo: assign, Text: "…", TextColor: color, Font: font, Background: panelBrush}
}

// --- dark title bar ---

var (
	dwmapi         = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetAttr = dwmapi.NewProc("DwmSetWindowAttribute")
)

func enableDarkTitleBar(hwnd uintptr) {
	if hwnd == 0 || procDwmSetAttr.Find() != nil {
		return
	}
	var on int32 = 1
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
		return
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
		if size > 60 {
			size = 60
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
	u.monoFont, _ = walk.NewFont("Consolas", 9, 0)
	u.hdrFont, _ = walk.NewFont("Segoe UI", 9, walk.FontBold)
	u.brushPanel, _ = walk.NewSolidColorBrush(cPanel)
	u.brushBg, _ = walk.NewSolidColorBrush(cBg)
	u.brushTrack, _ = walk.NewSolidColorBrush(cPanel2)
	u.brushAccent, _ = walk.NewSolidColorBrush(cAccent)
	u.brushHdr, _ = walk.NewSolidColorBrush(cHdr)
	u.brushGreen, _ = walk.NewSolidColorBrush(cGreen)
	u.brushYellow, _ = walk.NewSolidColorBrush(cYellow)
	u.brushRed, _ = walk.NewSolidColorBrush(cRed)
	u.penGrid, _ = walk.NewCosmeticPen(walk.PenSolid, cLine)
	u.penNet, _ = walk.NewCosmeticPen(walk.PenSolid, cNet)
	u.penGw, _ = walk.NewCosmeticPen(walk.PenSolid, cLan)
	u.penIsp, _ = walk.NewCosmeticPen(walk.PenSolid, cIsp)
}

func (u *ui) disposeGDI() {
	objs := []interface{ Dispose() }{u.smallFont, u.monoFont, u.hdrFont, u.brushPanel, u.brushBg, u.brushTrack, u.brushGreen, u.brushYellow, u.brushRed, u.brushAccent, u.brushHdr, u.penGrid, u.penNet, u.penGw, u.penIsp}
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
	if u.appIcon != nil {
		ni.SetIcon(u.appIcon)
	} else if ic := walk.IconApplication(); ic != nil {
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

func (u *ui) showWindow() { u.mw.Show(); u.mw.Activate() }

// paintTab draws one tab in the custom strip: centered title, muted unless
// active, with an accent underline on the active tab.
func (u *ui) paintTab(i int, canvas *walk.Canvas) error {
	if u.brushHdr == nil || u.tabHdrW[i] == nil {
		return nil
	}
	cb := u.tabHdrW[i].ClientBoundsPixels()
	canvas.FillRectanglePixels(u.brushHdr, walk.Rectangle{X: 0, Y: 0, Width: cb.Width, Height: cb.Height})
	col := cSub
	if i == u.activeTab {
		col = cText
	}
	if u.hdrFont != nil {
		canvas.DrawTextPixels(u.tabTitle[i], u.hdrFont, col,
			walk.Rectangle{X: 0, Y: 0, Width: cb.Width, Height: cb.Height},
			walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	}
	if i == u.activeTab && u.brushAccent != nil {
		canvas.FillRectanglePixels(u.brushAccent, walk.Rectangle{X: 0, Y: cb.Height - 2, Width: cb.Width, Height: 2})
	}
	return nil
}

// selectTab shows the chosen content panel, hides the rest, and repaints the
// strip so the active-tab styling updates.
func (u *ui) selectTab(i int) {
	if i < 0 || i >= len(u.tabContent) {
		return
	}
	u.activeTab = i
	vis := tabVisibility(i, len(u.tabContent))
	for j := range u.tabContent {
		if u.tabContent[j] != nil {
			u.tabContent[j].SetVisible(vis[j])
		}
	}
	for j := range u.tabHdrW {
		if u.tabHdrW[j] != nil {
			u.tabHdrW[j].Invalidate()
		}
	}
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
			u.bbStatus.SetText(fmt.Sprintf("Grade %s  (+%v under load, %.0f Mbps down)", res.Grade, res.Added.Round(time.Millisecond), res.DownloadMbps))
		})
	}()
}

func (u *ui) onClearEvents() {
	u.eng.ClearIssues()
	u.rebuildIssues(u.eng.Issues())
	u.issueDetail.SetText("Select an event above to see details.")
}

func (u *ui) render(s model.Snapshot) {
	v := s.Verdict
	u.statusPill.SetText(statusWord(v.Severity))
	u.statusPill.SetTextColor(severityColor(v.Severity))
	if v.Fix != "" && v.Culprit != model.CulpritHealthy {
		u.verdict.SetText(v.Headline + "  —  Fix: " + v.Fix)
	} else {
		u.verdict.SetText(v.Headline)
	}

	// Stat tiles from the best internet anchor.
	best := bestInternet(s)
	setTile := func(t statTile, val, sub string, r metrics.Rating) {
		t.val.SetText(val)
		t.val.SetTextColor(ratingColor(r))
		t.sub.SetText(sub)
		t.sub.SetTextColor(cSub)
	}
	if best != nil && best.Alive {
		st := best.Stats
		setTile(u.tiles[0], fmt.Sprintf("%d ms", st.Mean.Milliseconds()), ratingText(metrics.RateLatency(st.Mean)), metrics.RateLatency(st.Mean))
		setTile(u.tiles[1], fmt.Sprintf("%.1f ms", float64(st.Jitter)/float64(time.Millisecond)), ratingText(metrics.RateJitter(st.Jitter)), metrics.RateJitter(st.Jitter))
		setTile(u.tiles[2], fmt.Sprintf("%.1f %%", st.Loss*100), ratingText(metrics.RateLoss(st.Loss)), metrics.RateLoss(st.Loss))
	} else {
		for i := 0; i < 3; i++ {
			setTile(u.tiles[i], "—", "no data", metrics.RatingUnknown)
		}
	}
	if s.Bufferbloat != nil {
		g := s.Bufferbloat.Grade
		setTile(u.tiles[3], g, fmt.Sprintf("+%v under load", s.Bufferbloat.Added.Round(time.Millisecond)), metrics.GradeRating(g))
	} else {
		setTile(u.tiles[3], "—", "run the test", metrics.RatingUnknown)
	}

	// Path rings.
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

	// System bars.
	u.sCPU, u.sMem, u.sMemUsed, u.sMemTot, u.sGPU, u.sIn, u.sOut =
		s.Sys.CPUPercent, s.Sys.MemPercent, s.Sys.MemUsedGB, s.Sys.MemTotalGB, s.Sys.GPUPercent, s.Sys.InMbps, s.Sys.OutMbps
	u.sysBars.Invalidate()
	u.spark.Invalidate()

	u.renderConnection(s)
	u.renderDNS(s)

	if issues := u.eng.Issues(); len(issues) != len(u.issueData) {
		u.rebuildIssues(issues)
	}

	if v.Culprit != model.CulpritHealthy {
		u.tray.SetToolTip(fmt.Sprintf("Agent Smith — %s: %s", v.Severity, v.Culprit))
	} else {
		u.tray.SetToolTip("Agent Smith — connection healthy")
	}
}

func nz(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (u *ui) renderConnection(s model.Snapshot) {
	if c := s.Conn; c != nil {
		u.connISP.SetText(nz(c.ISP))
		u.connISP.SetTextColor(cText)
		u.connOrg.SetText(nz(c.Org))
		u.connIP.SetText(nz(c.IP))
		asn := c.ASN()
		if c.ASName != "" {
			asn += "  (" + c.ASName + ")"
		}
		u.connASN.SetText(nz(asn))
		u.connLoc.SetText(nz(c.Location()))
		u.connType.SetText(nz(c.ConnType))
		u.connRDNS.SetText(nz(c.Reverse))
		if c.Support != "" {
			u.connSupport.SetText(c.Support)
			u.connSupport.SetTextColor(cGreen)
		} else {
			u.connSupport.SetText("— (see your provider's site / bill)")
			u.connSupport.SetTextColor(cSub)
		}
		u.connOutage.SetText(nz(c.SupportURL))
	} else {
		u.connISP.SetText("looking up…")
		u.connISP.SetTextColor(cSub)
		for _, l := range []*walk.Label{u.connOrg, u.connIP, u.connASN, u.connLoc, u.connType, u.connRDNS, u.connSupport, u.connOutage} {
			l.SetText("…")
		}
	}
}

func (u *ui) renderDNS(s model.Snapshot) {
	for i := range u.dnsRows {
		r := u.dnsRows[i]
		if i >= len(s.DNSServers) {
			r.name.SetText("")
			r.addr.SetText("")
			r.avg.SetText("")
			r.status.SetText("")
			continue
		}
		d := s.DNSServers[i]
		r.name.SetText(d.Name)
		addr := d.Addr
		if addr == "" {
			addr = "(system)"
		}
		r.addr.SetText(addr)
		switch {
		case !d.OK():
			r.avg.SetText("—")
			r.avg.SetTextColor(cSub)
			r.status.SetText("fail")
			r.status.SetTextColor(cRed)
		case d.Slow():
			r.avg.SetText(rnd(d.Avg).String())
			r.avg.SetTextColor(cYellow)
			r.status.SetText("slow")
			r.status.SetTextColor(cYellow)
		default:
			r.avg.SetText(rnd(d.Avg).String())
			r.avg.SetTextColor(cGreen)
			r.status.SetText("ok")
			r.status.SetTextColor(cGreen)
		}
	}
}

func (u *ui) fillRow(r ringRow, ring string, ts *model.TargetStats) {
	r.ring.SetText(ring)
	r.target.SetText(trunc(ts.Name+" "+ts.Host, 24))
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

// paintSysBars draws CPU/Memory/GPU bars + a throughput line.
func (u *ui) paintSysBars(canvas *walk.Canvas, _ walk.Rectangle) error {
	if u.brushPanel == nil {
		return nil
	}
	cb := u.sysBars.ClientBoundsPixels()
	W, H := cb.Width, cb.Height
	canvas.FillRectanglePixels(u.brushPanel, walk.Rectangle{X: 0, Y: 0, Width: W, Height: H})

	type bar struct {
		label, extra string
		pct          float64
		ok           bool
	}
	bars := []bar{
		{"CPU", "", u.sCPU, true},
		{"Memory", fmt.Sprintf("%.1f / %.1f GB", u.sMemUsed, u.sMemTot), u.sMem, true},
		{"GPU", "", u.sGPU, u.sGPU >= 0},
	}
	pad := 4
	rowH := (H - 22) / 3
	for i, b := range bars {
		y := pad + i*rowH
		valStr := "n/a"
		if b.ok {
			valStr = fmt.Sprintf("%.0f%%", b.pct)
		}
		lbl := b.label
		if b.extra != "" {
			lbl += "  " + b.extra
		}
		if u.smallFont != nil {
			canvas.DrawTextPixels(lbl, u.smallFont, cSub, walk.Rectangle{X: pad, Y: y, Width: W - 60, Height: 14}, walk.TextLeft|walk.TextSingleLine)
			canvas.DrawTextPixels(valStr, u.monoFont, barColor(b.pct, b.ok), walk.Rectangle{X: W - 60 - pad, Y: y, Width: 60, Height: 14}, walk.TextRight|walk.TextSingleLine)
		}
		// track + fill
		ty := y + 18
		tw := W - 2*pad
		canvas.FillRoundedRectanglePixels(u.brushTrack, walk.Rectangle{X: pad, Y: ty, Width: tw, Height: 7}, walk.Size{Width: 4, Height: 4})
		if b.ok && b.pct > 0 {
			fw := int(float64(tw) * b.pct / 100)
			if fw < 4 {
				fw = 4
			}
			canvas.FillRoundedRectanglePixels(barBrush(u, b.pct), walk.Rectangle{X: pad, Y: ty, Width: fw, Height: 7}, walk.Size{Width: 4, Height: 4})
		}
	}
	if u.smallFont != nil {
		canvas.DrawTextPixels(fmt.Sprintf("Network   ↓ %.1f  /  ↑ %.1f Mbps", u.sIn, u.sOut), u.smallFont, cSub,
			walk.Rectangle{X: pad, Y: H - 16, Width: W - 2*pad, Height: 14}, walk.TextLeft|walk.TextSingleLine)
	}
	return nil
}

func barColor(pct float64, ok bool) walk.Color {
	if !ok {
		return cSub
	}
	switch {
	case pct >= 90:
		return cRed
	case pct >= 75:
		return cYellow
	default:
		return cGreen
	}
}

func barBrush(u *ui, pct float64) walk.Brush {
	switch {
	case pct >= 90:
		return u.brushRed
	case pct >= 75:
		return u.brushYellow
	default:
		return u.brushGreen
	}
}

// paintSpark draws the persisted RTT history with an area fill under Internet.
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
	max *= 1.2

	const pad, legendH = 8, 16
	plotW, plotH := W-2*pad, H-2*pad-legendH
	if plotW < 4 || plotH < 4 || len(net) < 2 {
		return nil
	}
	n := len(net)
	xAt := func(i int) int { return pad + i*plotW/(n-1) }
	yAt := func(v float64) int {
		y := pad + plotH - int(v/max*float64(plotH))
		if y < pad {
			y = pad
		}
		if y > pad+plotH {
			y = pad + plotH
		}
		return y
	}

	// gridlines + scale
	for _, f := range []float64{0, 0.5, 1} {
		yy := pad + int(float64(plotH)*f)
		canvas.DrawLinePixels(u.penGrid, walk.Point{X: pad, Y: yy}, walk.Point{X: pad + plotW, Y: yy})
		if u.smallFont != nil {
			canvas.DrawTextPixels(fmt.Sprintf("%.0f ms", max*(1-f)), u.smallFont, cSub, walk.Rectangle{X: pad + plotW - 50, Y: yy, Width: 50, Height: 12}, walk.TextRight|walk.TextSingleLine)
		}
	}

	// area fill under Internet (per-column vertical gradient → panel bg)
	baseline := pad + plotH
	for x := pad; x < pad+plotW; x += 2 {
		t := float64(x-pad) / float64(plotW) * float64(n-1)
		i := int(t)
		frac := t - float64(i)
		v := net[i]
		if i+1 < n {
			v = net[i]*(1-frac) + net[i+1]*frac
		}
		yl := yAt(v)
		if baseline-yl > 0 {
			canvas.GradientFillRectanglePixels(cArea, cPanel, walk.Vertical, walk.Rectangle{X: x, Y: yl, Width: 2, Height: baseline - yl})
		}
	}

	draw := func(s []float64, pen *walk.CosmeticPen) {
		pts := make([]walk.Point, n)
		for i, v := range s {
			pts[i] = walk.Point{X: xAt(i), Y: yAt(v)}
		}
		canvas.DrawPolylinePixels(pen, pts)
	}
	draw(gw, u.penGw)
	draw(isp, u.penIsp)
	draw(net, u.penNet)

	if u.smallFont != nil {
		ly := H - legendH + 2
		canvas.DrawTextPixels("● Internet", u.smallFont, cNet, walk.Rectangle{X: pad, Y: ly, Width: 90, Height: 12}, walk.TextLeft|walk.TextSingleLine)
		canvas.DrawTextPixels("● LAN", u.smallFont, cLan, walk.Rectangle{X: pad + 96, Y: ly, Width: 60, Height: 12}, walk.TextLeft|walk.TextSingleLine)
		canvas.DrawTextPixels("● ISP hop", u.smallFont, cIsp, walk.Rectangle{X: pad + 160, Y: ly, Width: 90, Height: 12}, walk.TextLeft|walk.TextSingleLine)
	}
	return nil
}

// --- events list ---

func (u *ui) rebuildIssues(issues []model.Issue) {
	data := make([]model.Issue, 0, len(issues))
	items := make([]*issueItem, 0, len(issues))
	for i := len(issues) - 1; i >= 0; i-- {
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
	_ = u.issueTable.SetModel(&issueModel{items: items})
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

func (u *ui) showIssueDetail() {
	idx := u.issueTable.CurrentIndex()
	if idx < 0 || idx >= len(u.issueData) {
		return
	}
	u.issueDetail.SetText(formatIssueDetail(u.issueData[idx]))
}

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
	fmt.Fprintf(&b, "%sWHAT THIS MEANS%s%s%s", nl, nl, wordWrap(interpret.Summary(is), 80, "  "), nl)
	fmt.Fprintf(&b, "%sMEASUREMENTS%s", nl, nl)
	for _, l := range interpret.Lines(is.Metrics) {
		fmt.Fprintf(&b, "  %-14s %-18s %-10s %s%s", l.Label, l.Value, l.Rating, l.Meaning, nl)
	}
	if is.Fix != "" {
		fmt.Fprintf(&b, "%sSUGGESTED FIX%s%s%s", nl, nl, wordWrap(is.Fix, 80, "  "), nl)
	}
	if len(is.Procs) > 0 {
		fmt.Fprintf(&b, "%sTOP PROCESSES (ps snapshot)%s", nl, nl)
		for _, p := range is.Procs {
			fmt.Fprintf(&b, "  %-26s %5.0f%% CPU  %7.0f MB%s", trunc(p.Name, 26), p.CPU, p.MemMB, nl)
		}
	}
	return b.String()
}

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

// --- helpers ---

func bestInternet(s model.Snapshot) *model.TargetStats {
	var best *model.TargetStats
	for i := range s.Internet {
		ts := &s.Internet[i]
		if !ts.Alive {
			continue
		}
		if best == nil || ts.Stats.Mean < best.Stats.Mean {
			best = ts
		}
	}
	if best == nil && len(s.Internet) > 0 {
		return &s.Internet[0]
	}
	return best
}

func statusWord(s model.Severity) string {
	switch s {
	case model.SevOK:
		return "● OPERATIONAL"
	case model.SevWatch:
		return "● WATCH"
	case model.SevDegraded:
		return "● DEGRADED"
	case model.SevCritical:
		return "● CRITICAL"
	default:
		return "● —"
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

func ratingText(r metrics.Rating) string {
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
