//go:build windows

// Package gui implements the native Windows interface for Agent Smith using
// lxn/walk: a dashboard window plus a system-tray icon. The window shows the
// live verdict, the concentric-ring path metrics, local diagnostics, and a
// button to run an on-demand bufferbloat test. Closing the window hides it to
// the tray; quitting is done from the tray menu.
package gui

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/lxn/walk"
	decl "github.com/lxn/walk/declarative"

	"github.com/NYBaywatch/agent-smith/internal/bufferbloat"
	"github.com/NYBaywatch/agent-smith/internal/engine"
	"github.com/NYBaywatch/agent-smith/internal/model"
)

type ui struct {
	mw        *walk.MainWindow
	verdict   *walk.Label
	detail    *walk.Label
	fix       *walk.Label
	metrics   *walk.TextEdit
	bbButton  *walk.PushButton
	bbStatus  *walk.Label
	tray      *walk.NotifyIcon
	eng       *engine.Engine
	ctx       context.Context
	bbRunning bool
}

// Run builds and runs the GUI, blocking until the user quits or ctx is cancelled.
func Run(ctx context.Context, e *engine.Engine) error {
	runtime.LockOSThread()

	u := &ui{eng: e, ctx: ctx}

	mono := decl.Font{Family: "Consolas", PointSize: 10}
	if err := (decl.MainWindow{
		AssignTo: &u.mw,
		Title:    "Agent Smith — connection monitor for gamers",
		MinSize:  decl.Size{Width: 620, Height: 520},
		Size:     decl.Size{Width: 660, Height: 560},
		Layout:   decl.VBox{MarginsZero: false},
		Children: []decl.Widget{
			decl.Label{AssignTo: &u.verdict, Text: "Starting Agent Smith…", Font: decl.Font{PointSize: 13, Bold: true}},
			decl.Label{AssignTo: &u.detail, Text: "", MaxSize: decl.Size{Width: 640}},
			decl.Label{AssignTo: &u.fix, Text: "", TextColor: walk.RGB(0xB8, 0x86, 0x00)},
			decl.GroupBox{
				Title:  "Path — concentric rings (LAN → ISP edge → internet)",
				Layout: decl.VBox{},
				Children: []decl.Widget{
					decl.TextEdit{AssignTo: &u.metrics, ReadOnly: true, Font: mono, VScroll: true},
				},
			},
			decl.Composite{
				Layout: decl.HBox{MarginsZero: true},
				Children: []decl.Widget{
					decl.PushButton{
						AssignTo:  &u.bbButton,
						Text:      "Run Bufferbloat Test",
						OnClicked: u.onBufferbloat,
					},
					decl.Label{AssignTo: &u.bbStatus, Text: ""},
					decl.HSpacer{},
				},
			},
		},
	}).Create(); err != nil {
		return err
	}

	if err := u.setupTray(); err != nil {
		return err
	}
	defer u.tray.Dispose()

	// Hide to tray on close instead of exiting.
	u.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if reason == walk.CloseReasonUnknown { // user pressed the X
			*canceled = true
			u.mw.Hide()
			u.tray.ShowInfo("Agent Smith", "Still watching your connection in the tray.")
		}
	})

	// Feed snapshots into the UI thread.
	go u.consume(ctx)

	// Quit cleanly when the context is cancelled.
	go func() {
		<-ctx.Done()
		u.mw.Synchronize(func() { walk.App().Exit(0) })
	}()

	u.render(e.Latest())
	u.mw.Run()
	return nil
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

	show := walk.NewAction()
	show.SetText("&Show dashboard")
	show.Triggered().Attach(func() { u.showWindow() })
	ni.ContextMenu().Actions().Add(show)

	test := walk.NewAction()
	test.SetText("Run &bufferbloat test")
	test.Triggered().Attach(func() { u.showWindow(); u.onBufferbloat() })
	ni.ContextMenu().Actions().Add(test)

	sep := walk.NewSeparatorAction()
	ni.ContextMenu().Actions().Add(sep)

	quit := walk.NewAction()
	quit.SetText("&Quit")
	quit.Triggered().Attach(func() { walk.App().Exit(0) })
	ni.ContextMenu().Actions().Add(quit)

	// Left-click the tray icon shows the window.
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
		// Derive from the app context so shutdown cancels an in-flight test.
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
	u.verdict.SetText(badge(v.Severity) + "  " + v.Headline)
	u.verdict.SetTextColor(severityColor(v.Severity))
	u.detail.SetText(v.Detail)
	if v.Fix != "" {
		u.fix.SetText("Fix: " + v.Fix)
	} else {
		u.fix.SetText("")
	}
	if v.Culprit != model.CulpritHealthy {
		u.tray.SetToolTip(fmt.Sprintf("Agent Smith — %s: %s", v.Severity, v.Culprit))
	} else {
		u.tray.SetToolTip("Agent Smith — connection healthy")
	}
	u.metrics.SetText(formatMetrics(s))
}

func formatMetrics(s model.Snapshot) string {
	var b strings.Builder
	nl := "\r\n" // walk TextEdit wants CRLF
	fmt.Fprintf(&b, "%-4s %-22s %-9s %-9s %-9s %-6s"+nl, "Ring", "Target", "avg", "p95", "jitter", "loss")
	fmt.Fprintf(&b, "%s"+nl, strings.Repeat("─", 62))
	row := func(ring string, ts *model.TargetStats) {
		if ts == nil {
			return
		}
		if !ts.Alive {
			fmt.Fprintf(&b, "%-4s %-22s no reply"+nl, ring, trunc(ts.Name+" "+ts.Host, 22))
			return
		}
		st := ts.Stats
		fmt.Fprintf(&b, "%-4s %-22s %-9s %-9s %-9s %4.1f%%"+nl,
			ring, trunc(ts.Name+" "+ts.Host, 22),
			rnd(st.Mean), rnd(st.P95), rnd(st.Jitter), st.Loss*100)
	}
	row("LAN", s.Gateway)
	row("ISP", s.ISPHop)
	for i := range s.Internet {
		label := "NET"
		if i > 0 {
			label = ""
		}
		row(label, &s.Internet[i])
	}

	b.WriteString(nl)
	if s.Net != nil && s.Net.Active != nil {
		a := s.Net.Active
		fmt.Fprintf(&b, "Interface : %s (%s, %d Mbps, MTU %d)"+nl, a.Name, a.Media, a.LinkMbps, a.MTU)
		if errs := a.InErrors + a.OutErrors + a.InDiscards + a.OutDiscards; errs > 0 {
			fmt.Fprintf(&b, "NIC errors: %d"+nl, errs)
		}
	}
	if s.Net != nil && s.Net.WiFi != nil {
		w := s.Net.WiFi
		fmt.Fprintf(&b, "Wi-Fi     : %q  %d dBm (%d%%)  rx %.0f / tx %.0f Mbps"+nl,
			w.SSID, w.RSSI, w.SignalQuality, w.RxMbps, w.TxMbps)
	}
	fmt.Fprintf(&b, "Resources : CPU %.0f%%   Mem %.0f%%   net down %.1f / up %.1f Mbps"+nl,
		s.Sys.CPUPercent, s.Sys.MemPercent, s.Sys.InMbps, s.Sys.OutMbps)
	if s.DNS.Lookups > 0 {
		fmt.Fprintf(&b, "DNS       : %v avg"+nl, rnd(s.DNS.Avg))
	}
	if s.Bufferbloat != nil {
		bb := s.Bufferbloat
		fmt.Fprintf(&b, "Bufferbloat: grade %s (+%v under load, %.0f Mbps down)"+nl,
			bb.Grade, bb.Added.Round(time.Millisecond), bb.DownloadMbps)
	}
	return b.String()
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
		return walk.RGB(0x1E, 0x8E, 0x3E)
	case model.SevWatch:
		return walk.RGB(0xB8, 0x86, 0x00)
	default:
		return walk.RGB(0xC5, 0x22, 0x1F)
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
