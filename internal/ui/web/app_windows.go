//go:build windows

package web

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/gen2brain/beeep"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/NYBaywatch/agent-smith/internal/config"
	"github.com/NYBaywatch/agent-smith/internal/engine"
	"github.com/NYBaywatch/agent-smith/internal/incident"
	"github.com/NYBaywatch/agent-smith/internal/model"
	"github.com/NYBaywatch/agent-smith/internal/store"
)

//go:embed all:frontend
var assets embed.FS

//go:embed app.ico
var appICO []byte

// App is the struct bound into the page as window.go.web.App. Every exported
// method is callable from JavaScript; none of them return Go errors (the page
// gets a result object with an error string instead).
type App struct {
	ctx     context.Context
	eng     *engine.Engine
	version string
	started time.Time

	mu       sync.Mutex
	testBusy string // name of the on-demand test in flight, "" when idle
	quitting bool
	iconPath string
}

// uiTestHook is set only by `-tags uitest` builds (see uitest_windows.go) and
// receives the Wails context once the window exists.
var uiTestHook func(ctx context.Context)

// Run builds the window, tray and notification plumbing and blocks until the
// user quits or ctx is cancelled.
func Run(ctx context.Context, eng *engine.Engine, version string) error {
	app := &App{eng: eng, version: version, started: time.Now()}
	beeep.AppName = "Agent Smith"

	err := wails.Run(&options.App{
		Title:              "Agent Smith",
		Width:              440,
		Height:             880,
		MinWidth:           380,
		MinHeight:          620,
		Frameless:          true,
		BackgroundColour:   &options.RGBA{R: 0x0e, G: 0x11, B: 0x16, A: 255},
		AssetServer:        &assetserver.Options{Assets: assets},
		Bind:               []interface{}{app},
		LogLevelProduction: logger.ERROR,
		OnStartup: func(wctx context.Context) {
			app.mu.Lock()
			app.ctx = wctx
			app.mu.Unlock()
			go app.pumpSnapshots(ctx)
			go app.pumpIncidents(ctx)
			go app.runTray(ctx)
			if uiTestHook != nil {
				uiTestHook(wctx)
			}
			go func() {
				<-ctx.Done()
				app.setQuitting()
				runtime.Quit(wctx)
			}()
		},
		OnBeforeClose: func(wctx context.Context) bool {
			app.mu.Lock()
			q := app.quitting
			app.mu.Unlock()
			if q {
				return false
			}
			runtime.WindowHide(wctx)
			return true
		},
		Windows: &windows.Options{
			IsZoomControlEnabled:              true,
			Theme:                             windows.Dark,
			DisableFramelessWindowDecorations: false,
			WebviewIsTransparent:              false,
		},
	})
	systray.Quit()
	return err
}

func (a *App) context() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ctx
}

func (a *App) setQuitting() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
}

// --- bound methods ---

// Snapshot returns the current engine view.
func (a *App) Snapshot() Snapshot { return BuildSnapshot(a.eng.Latest(), a.eng.SLO()) }

// History returns downsampled RTT history (≤ 600 points).
func (a *App) History() []HistPoint { return BuildHistory(a.eng.History(), 600) }

// Issues returns recorded events, newest first.
func (a *App) Issues() []IssueDTO { return BuildIssues(a.eng.Issues()) }

// Incidents returns grouped incidents, newest first.
func (a *App) Incidents() []IncidentDTO { return BuildIncidents(a.eng.Incidents(), time.Now()) }

// Info describes the running app.
func (a *App) Info() Info {
	cp, _ := config.FilePath()
	sp, _ := store.Path()
	return Info{Version: a.version, ConfigPath: cp, StatePath: sp, Started: a.started.Format(time.RFC3339)}
}

// RunChecks executes every synthetic check now and returns the fresh results.
func (a *App) RunChecks() []ServiceDTO {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return BuildServices(a.eng.RunSyntheticsNow(ctx))
}

// Trace runs an on-demand enriched traceroute to host.
func (a *App) Trace(host string) PathDTO {
	host = strings.TrimSpace(host)
	if host == "" {
		return PathDTO{Hops: []HopDTO{}, Diagnosis: Diagnosis{HopIndex: -1}, Error: "no host given"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, err := a.eng.TraceNow(ctx, host, host)
	if err != nil && len(p.Hops) == 0 {
		return PathDTO{Name: host, Dest: host, Hops: []HopDTO{}, Diagnosis: Diagnosis{HopIndex: -1}, Error: err.Error()}
	}
	d := pathmonDiag(p)
	out := BuildPath(p, d)
	if err != nil {
		out.Error = err.Error()
	}
	return out
}

// ClearIssues empties the event log.
func (a *App) ClearIssues() { a.eng.ClearIssues() }

// ClearIncidents empties the incident list.
func (a *App) ClearIncidents() { a.eng.ClearIncidents() }

// Minimise minimises the window.
func (a *App) Minimise() {
	if ctx := a.context(); ctx != nil {
		runtime.WindowMinimise(ctx)
	}
}

// Hide hides the window to the tray.
func (a *App) Hide() {
	if ctx := a.context(); ctx != nil {
		runtime.WindowHide(ctx)
	}
}

// Quit exits the application.
func (a *App) Quit() {
	a.setQuitting()
	if ctx := a.context(); ctx != nil {
		runtime.Quit(ctx)
	}
}

// OpenURL opens an http(s) URL in the default browser.
func (a *App) OpenURL(url string) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return
	}
	if ctx := a.context(); ctx != nil {
		runtime.BrowserOpenURL(ctx, url)
	}
}

// --- background pumps ---

func (a *App) show() {
	if ctx := a.context(); ctx != nil {
		runtime.WindowUnminimise(ctx)
		runtime.WindowShow(ctx)
	}
}

func (a *App) pumpSnapshots(ctx context.Context) {
	ch := a.eng.Subscribe()
	slo := a.eng.SLO()
	for {
		select {
		case <-ctx.Done():
			return
		case snap := <-ch:
			wctx := a.context()
			if wctx == nil {
				continue
			}
			runtime.EventsEmit(wctx, "snapshot", BuildSnapshot(snap, slo))
			systray.SetTooltip(trayTooltip(snap))
		}
	}
}

func trayTooltip(snap model.Snapshot) string {
	v := snap.Verdict
	if v.Culprit != model.CulpritHealthy {
		return fmt.Sprintf("Agent Smith — %s: %s", v.Severity, v.Culprit)
	}
	return "Agent Smith — connection healthy"
}

type incidentEvent struct {
	Opened *IncidentDTO `json:"opened"`
	Closed *IncidentDTO `json:"closed"`
}

func (a *App) pumpIncidents(ctx context.Context) {
	ch := a.eng.IncidentEvents()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			now := time.Now()
			var out incidentEvent
			if ev.Opened != nil {
				d := BuildIncident(*ev.Opened, now)
				out.Opened = &d
			}
			if ev.Closed != nil {
				d := BuildIncident(*ev.Closed, now)
				out.Closed = &d
			}
			if wctx := a.context(); wctx != nil {
				runtime.EventsEmit(wctx, "incident", out)
			}
			if a.eng.NotifyEnabled() {
				a.notify(ev)
			}
		}
	}
}

func (a *App) notify(ev incident.Event) {
	icon := a.iconFile()
	if ev.Opened != nil {
		_ = beeep.Notify("Agent Smith — "+ev.Opened.Culprit.String(), ev.Opened.Headline, icon)
	}
	if ev.Closed != nil {
		_ = beeep.Notify("Agent Smith — resolved",
			fmt.Sprintf("%s (lasted %s)", ev.Closed.Headline, ev.Closed.Duration(time.Now()).Round(time.Second)), icon)
	}
}

// iconFile writes the embedded icon to a temp file once and returns its path
// ("" when that fails; toasts then show without an icon).
func (a *App) iconFile() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.iconPath != "" {
		return a.iconPath
	}
	p := filepath.Join(os.TempDir(), "agent-smith-icon.ico")
	if err := os.WriteFile(p, appICO, 0o644); err != nil {
		return ""
	}
	a.iconPath = p
	return p
}

// --- tray ---

func (a *App) runTray(ctx context.Context) {
	systray.Run(func() {
		systray.SetIcon(appICO)
		systray.SetTooltip("Agent Smith")
		systray.SetOnTapped(a.show)

		show := systray.AddMenuItem("Show dashboard", "Open the Agent Smith window")
		checks := systray.AddMenuItem("Run service checks", "Run every synthetic HTTP check now")
		bb := systray.AddMenuItem("Run bufferbloat test", "Saturate the link and grade latency under load")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit", "Exit Agent Smith")

		go a.onClick(ctx, show, a.show)
		go a.onClick(ctx, checks, func() {
			res := a.RunChecks()
			if wctx := a.context(); wctx != nil {
				runtime.EventsEmit(wctx, "checks-done", res)
			}
		})
		go a.onClick(ctx, bb, func() {
			a.show()
			res := a.RunBufferbloat()
			if wctx := a.context(); wctx != nil {
				runtime.EventsEmit(wctx, "bufferbloat-done", res)
			}
		})
		go a.onClick(ctx, quit, a.Quit)
	}, nil)
}

func (a *App) onClick(ctx context.Context, item *systray.MenuItem, fn func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-item.ClickedCh:
			go fn()
		}
	}
}
