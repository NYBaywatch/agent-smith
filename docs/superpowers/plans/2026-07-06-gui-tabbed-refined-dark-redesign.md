# Agent Smith GUI Redesign (Tabbed + Refined Dark) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rework the Agent Smith GUI from six vertically-stacked collapsible sections into a pinned summary plus a custom, dark-styled tab strip, on a consistent "Refined Dark" token set.

**Architecture:** All work is confined to `internal/ui/gui/gui_windows.go` (a single `//go:build windows` file using `lxn/walk`). We keep the RTT chart + status + stat tiles pinned at the top, turn the remaining five sections (Path, Connection, System, DNS, Events) into tabs driven by a hand-painted tab strip (reusing the existing `CustomWidget`/`DrawTextPixels` pattern — deliberately **not** walk's native `TabWidget`, whose light native headers would clash with the dark theme), and pin the action bar at the bottom. `render()`, font scaling, tray, and all painting stay behavior-identical; only the container arrangement and the color/spacing tokens change.

**Tech Stack:** Go, `github.com/lxn/walk` (+ `declarative`), Win32/GDI painting, `golang.org/x/sys/windows`.

## Global Constraints

- **Go toolchain is not on PATH.** Invoke the compiler by absolute path: `"/c/Program Files/Go/bin/go.exe"` (Git Bash) — copy commands verbatim. `-race` needs cgo; do not use it here.
- **Windows-only build tag.** `gui_windows.go` starts with `//go:build windows`. Any new `.go` file in package `gui` that references `walk` must carry the same `//go:build windows` tag. The pure-logic helper in Task 2 also carries it (the package only ever builds on Windows).
- **Single file of scope.** Do not modify `engine`, `metrics`, `model`, `interpret`, or probing. The only non-GUI change (rolling window `30→15`) is already committed.
- **No behavior regressions.** Tray show/hide, `Ctrl`+mouse-wheel font scaling, bufferbloat flow, and event-row drill-down must all still work after every task.
- **No automated UI harness exists.** For GUI-restructure tasks the "test" step is `go build` + a scripted manual pass. Only Task 2 has a true unit test. Run `"/c/Program Files/Go/bin/go.exe" test ./...` as a regression guard on every task regardless.
- **Branch:** work on `gui-redesign` (already checked out).

---

### Task 1: Refined Dark color tokens

Retune the package-level palette and add an accent + tab-strip brush. Layout is untouched this task — only colors change — so it's independently reviewable (the app runs exactly as before, just recolored).

**Files:**
- Modify: `internal/ui/gui/gui_windows.go` (color vars ~33-46; `initGDI` ~481-495; `disposeGDI` ~497-507)

**Interfaces:**
- Produces: package-level `walk.Color` vars `cAccent`, `cHdr`; `*ui` fields `brushAccent`, `brushHdr` (both `*walk.SolidColorBrush`) initialized in `initGDI`, disposed in `disposeGDI`. Consumed by Task 3's `paintTab`.

- [ ] **Step 1: Retune the palette vars**

In the `var ( … )` color block (currently lines ~33-45), replace the first four entries and add `cAccent` + `cHdr`. Final block:

```go
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
```

- [ ] **Step 2: Declare the two new brush fields**

In the `type ui struct` cached-GDI block (currently ~163-166), add the two brushes to the existing brush field line:

```go
	brushPanel, brushTrack, brushBg   *walk.SolidColorBrush
	brushGreen, brushYellow, brushRed *walk.SolidColorBrush
	brushAccent, brushHdr             *walk.SolidColorBrush
```

- [ ] **Step 3: Initialize and dispose the new brushes**

In `initGDI` add after `u.brushTrack` is set:

```go
	u.brushAccent, _ = walk.NewSolidColorBrush(cAccent)
	u.brushHdr, _ = walk.NewSolidColorBrush(cHdr)
```

In `disposeGDI`, add `u.brushAccent, u.brushHdr` to the `objs` slice:

```go
	objs := []interface{ Dispose() }{u.smallFont, u.monoFont, u.hdrFont, u.brushPanel, u.brushBg, u.brushTrack, u.brushGreen, u.brushYellow, u.brushRed, u.brushAccent, u.brushHdr, u.penGrid, u.penNet, u.penGw, u.penIsp}
```

- [ ] **Step 4: Build and regression-test**

Run:
```bash
"/c/Program Files/Go/bin/go.exe" build ./... && "/c/Program Files/Go/bin/go.exe" test ./...
```
Expected: build succeeds; all existing tests PASS (metrics/interpret unaffected).

- [ ] **Step 5: Manual smoke**

Run:
```bash
"/c/Program Files/Go/bin/go.exe" run ./cmd/agent-smith
```
(If the main package path differs, use the actual one, e.g. `./cmd/...` — find it with `ls cmd`.) Expected: window opens with the slightly deeper background and unchanged layout; nothing renders black-on-black or invisible. Close the window.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/gui/gui_windows.go
git commit -m "GUI: introduce Refined Dark accent + header tokens" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Tab visibility helper (TDD)

Extract the pure state logic behind tab switching so it has a real unit test — the one genuinely testable seam in this GUI work. Task 3's `selectTab` calls it.

**Files:**
- Create: `internal/ui/gui/tabs.go`
- Test: `internal/ui/gui/tabs_test.go`

**Interfaces:**
- Produces: `func tabVisibility(active, n int) []bool` — returns a length-`n` slice, `true` only at index `active` when `0 <= active < n`, all `false` otherwise. Consumed by Task 3's `selectTab`.

- [ ] **Step 1: Write the failing test**

Create `internal/ui/gui/tabs_test.go`:

```go
//go:build windows

package gui

import "testing"

func TestTabVisibility(t *testing.T) {
	got := tabVisibility(2, 5)
	want := []bool{false, false, true, false, false}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestTabVisibilityOutOfRange(t *testing.T) {
	// active out of range => nothing visible, but slice still length n.
	got := tabVisibility(9, 3)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, v := range got {
		if v {
			t.Fatalf("index %d visible, want none visible: %v", i, got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:
```bash
"/c/Program Files/Go/bin/go.exe" test ./internal/ui/gui/ -run TestTabVisibility -v
```
Expected: FAIL — `undefined: tabVisibility`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/ui/gui/tabs.go`:

```go
//go:build windows

package gui

// tabVisibility reports which of n stacked content panels should be visible
// when the tab at index `active` is selected. Exactly one panel is visible
// (the active one); if active is out of range, none are.
func tabVisibility(active, n int) []bool {
	out := make([]bool, n)
	if active >= 0 && active < n {
		out[active] = true
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run:
```bash
"/c/Program Files/Go/bin/go.exe" test ./internal/ui/gui/ -run TestTabVisibility -v
```
Expected: PASS (both cases).

- [ ] **Step 5: Commit**

```bash
git add internal/ui/gui/tabs.go internal/ui/gui/tabs_test.go
git commit -m "GUI: add tested tabVisibility helper for tab switching" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Pinned summary + custom tab strip (core restructure)

Replace the six collapsible sections with a pinned summary (status/verdict/tiles/chart) + a painted tab strip driving the five content panels (Path, Connection, System, DNS, Events). This maps cleanly onto the existing code: old collapsible section 0 (chart) becomes pinned; old sections 1-5 become the five tabs.

**Files:**
- Modify: `internal/ui/gui/gui_windows.go` — `ui` struct fields (~124-129), `Run()` (`secHeader` builder ~259-269 and the `Children` slice ~279-350), remove `paintHeader` (~542-558) and `toggleSection` (~560-568), add `paintTab` + `selectTab`.

**Interfaces:**
- Consumes: `cHdr`, `cAccent`, `u.brushHdr`, `u.brushAccent` (Task 1); `tabVisibility` (Task 2); existing `u.hdrFont`, `u.brushBg`; existing child slices `ringChildren`, `connChildren`, `dnsChildren`, the `u.sysBars` widget, and the Events `TableView`/`TextEdit`.
- Produces: `*ui` fields `tabHdrW [5]*walk.CustomWidget`, `tabContent [5]*walk.Composite`, `tabTitle [5]string`, `activeTab int`; methods `paintTab(i int, canvas *walk.Canvas) error` and `selectTab(i int)`.

- [ ] **Step 1: Swap the collapsible fields for tab fields**

In `type ui struct`, replace the collapsible-sections block (currently):

```go
	// collapsible sections (chart, path, connection, system, dns, events)
	secHdrW    [6]*walk.CustomWidget
	secContent [6]*walk.Composite
	secOpen    [6]bool
	secTitle   [6]string
```

with:

```go
	// custom tab strip (path, connection, system, dns, events)
	tabHdrW    [5]*walk.CustomWidget
	tabContent [5]*walk.Composite
	tabTitle   [5]string
	activeTab  int
```

- [ ] **Step 2: Delete the collapsible-section functions**

Delete `paintHeader` (the `func (u *ui) paintHeader(i int, canvas *walk.Canvas) error { … }` block, ~542-558) and `toggleSection` (~560-568) in their entirety. They are replaced by `paintTab`/`selectTab` in Step 3.

- [ ] **Step 3: Add `paintTab` and `selectTab`**

Add these two methods (e.g. where `paintHeader`/`toggleSection` used to be):

```go
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
```

- [ ] **Step 4: Remove the `secHeader` builder and build the tab strip instead**

Delete the `secHeader := func(i int, title, tip string) decl.CustomWidget { … }` closure (~259-269). In its place, before the `decl.MainWindow{…}` literal, add the tab-strip builder:

```go
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
```

- [ ] **Step 5: Rewrite the MainWindow `Children` slice**

Replace the entire `Children: []decl.Widget{ … }` of the `decl.MainWindow` (the block containing the six `secHeader(…)`/`secContent` sections, ~279-350) with the following. The header, verdict, and tiles composites are copied verbatim from the current code; the chart is now pinned (no collapsible header); the five sections become `tabContent` panels with only Path visible initially:

```go
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
			}},

			// Actions (always visible).
			decl.Composite{Background: bgBrush, Layout: decl.HBox{MarginsZero: true, Spacing: 8}, Children: []decl.Widget{
				decl.PushButton{AssignTo: &u.bbButton, Text: "Run Bufferbloat Test", OnClicked: u.onBufferbloat,
					ToolTipText: "Saturate your download for ~10 s and grade the added latency."},
				decl.Label{AssignTo: &u.bbStatus, Text: "", TextColor: cSub, Background: bgBrush},
				decl.HSpacer{},
			}},
		},
```

Note: the old "Clear Events" button is intentionally dropped from the action bar here; Task 4 re-adds it inside the Events tab. The old `mono` and `panelBrush`/`bgBrush` locals referenced above already exist in `Run()`.

- [ ] **Step 6: Force the initial tab after window creation**

Immediately after `u.initGDI()` … `enableDarkTitleBar(...)` and before `collectLabels(...)` (around line 358-360), add:

```go
	u.selectTab(0)
```

This guarantees the visibility/active-styling state is coherent regardless of declarative `Visible` defaults.

- [ ] **Step 7: Build and regression-test**

Run:
```bash
"/c/Program Files/Go/bin/go.exe" build ./... && "/c/Program Files/Go/bin/go.exe" test ./...
```
Expected: build succeeds (no references to `secHdrW`/`secContent`/`secOpen`/`secTitle`/`paintHeader`/`toggleSection` remain — if the compiler flags any, delete the leftover usage); tests PASS.

- [ ] **Step 8: Manual verification pass**

Run the app (`"/c/Program Files/Go/bin/go.exe" run ./cmd/agent-smith`) and confirm:
- Top area shows status pill + verdict + 4 tiles + RTT chart, all pinned.
- A five-item tab strip (PATH/CONNECTION/SYSTEM/DNS/EVENTS) sits below the chart; PATH is active (accent underline, brighter text).
- Clicking each tab swaps the panel below and moves the underline; no scrolling needed to reach DNS/Events.
- `Ctrl`+mouse-wheel still scales fonts across tiles and the active tab.
- Clicking an event row still fills the detail pane.
- Closing the window hides to tray; tray click restores.

- [ ] **Step 9: Commit**

```bash
git add internal/ui/gui/gui_windows.go
git commit -m "GUI: replace stacked collapsibles with pinned summary + tab strip" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Events-tab Clear button + spacing standardization

Re-home "Clear Events" inside the Events tab and standardize the tile/section padding to one scale so alignment reads clean (the owner's "weird spacing / not lined up" complaint).

**Files:**
- Modify: `internal/ui/gui/gui_windows.go` — Events `tabContent[4]` children (from Task 3), stat-tile `Composite` margins (~193), tile grid + window margins.

**Interfaces:**
- Consumes: `u.onClearEvents` (existing handler, unchanged), Events `tabContent[4]` from Task 3.
- Produces: no new exported surface.

- [ ] **Step 1: Add "Clear Events" into the Events tab**

In the Events tab `Composite` (`tabContent[4]`, from Task 3 Step 5), append a bottom action row after the `TextEdit`. The tab's `Children` becomes:

```go
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
```

- [ ] **Step 2: Standardize the stat-tile padding**

In the tile builder loop (currently `Layout: decl.VBox{Margins: mg(13, 11, 13, 11), Spacing: 4}`, ~193), change the margins to the standard card padding:

```go
				Layout:      decl.VBox{Margins: mg(12, 10, 12, 10), Spacing: 4},
```

- [ ] **Step 3: Build and regression-test**

Run:
```bash
"/c/Program Files/Go/bin/go.exe" build ./... && "/c/Program Files/Go/bin/go.exe" test ./...
```
Expected: build succeeds; tests PASS.

- [ ] **Step 4: Manual verification**

Run the app and confirm:
- The Events tab now shows a right-aligned "Clear Events" button under the detail pane; clicking it empties the list and detail.
- The action bar at the bottom now holds only "Run Bufferbloat Test" + status.
- Tiles and panels line up on a consistent margin; no ragged gaps.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/gui/gui_windows.go
git commit -m "GUI: move Clear Events into Events tab; standardize tile padding" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:**
- Pinned summary + tab strip layout → Task 3. ✓
- Custom painted tab strip (not native `TabWidget`) → Task 3 (`paintTab`/`selectTab`, `CustomWidget`). ✓
- Refined Dark palette tokens → Task 1. ✓
- Color-only-for-status rule → preserved (rating/severity helpers untouched; new accent used only for active-tab indicator). ✓
- 8px-grid / consistent spacing → Task 4 (tile padding standardized; tab restructure removes the misaligned collapsible headers). ✓
- Type ramp / Semibold → existing fonts retained; no heavy-bold added. Fonts already defined in `Run()`; no change required, so no task. ✓
- Remove collapsible machinery (`secHeader`/`paintHeader`/`toggleSection`, `sec*` fields) → Task 3 Steps 1-2. ✓
- Keep `render()`, `collectLabels`/font scaling, tray, painting → untouched by all tasks; verified in Task 3 Step 8. ✓
- "Clear Events" moves into Events tab → Task 4. ✓
- Window `30→15` → already committed; noted as out-of-scope for these tasks. ✓
- Verification = build + manual (+ tests as guard) → every task. ✓

**Placeholder scan:** No TBD/TODO; every code step shows complete code; commands are exact with expected output. ✓

**Type consistency:** `tabVisibility(active, n int) []bool` defined in Task 2, consumed identically in Task 3 `selectTab`. Fields `tabHdrW`/`tabContent`/`tabTitle`/`activeTab` declared in Task 3 Step 1 and used consistently in `paintTab`/`selectTab` and the `Children` slice. Brushes `brushAccent`/`brushHdr` declared/initialized in Task 1, consumed in Task 3 `paintTab`. ✓

_Note: exact source line numbers are from the pre-Task-1 file and will drift as tasks land; locate by symbol/nearby code, not line number._
