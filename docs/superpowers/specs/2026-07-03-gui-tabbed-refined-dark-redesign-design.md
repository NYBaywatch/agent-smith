# Agent Smith GUI Redesign — Tabbed Layout + "Refined Dark" Style

**Date:** 2026-07-03
**Scope:** UI layer only — `internal/ui/gui/gui_windows.go` (Windows/`lxn/walk`)
**Status:** Approved design, ready for implementation planning

## Problem

The current GUI is a single tall window of six vertically-stacked, collapsible
sections (RTT chart, Path, Connection, System, DNS, Events). The owner's
complaints, in their words:

- **Looks dated / unpolished.**
- **Weird, inconsistent spacing; empty space that doesn't line up.**
- **Hard to reach the bottom sections** (DNS, Events) — they require scrolling
  down a ~980px-tall stack.

The information shown is *not* the problem — the owner is happy with what's
measured and displayed. This is a **layout and visual-polish** redesign, not an
information-architecture rethink or a data/engine change.

## Goals

1. Make every panel reachable without scrolling to the bottom of a long stack.
2. Establish one consistent spacing grid and type ramp so nothing looks
   misaligned.
3. Modernize the look ("not dated") while keeping the dark, always-on,
   gaming-friendly character.
4. Keep the change contained to a single UI file with no behavior regressions
   (tray, font scaling, bufferbloat, event drill-down all still work).

## Non-goals (explicitly out of scope)

- No new metrics or "use-case scores" (e.g. Gaming/Calls/Streaming ratings).
  Surfaced in research, not requested — a possible future pass.
- No light theme / Windows 11 Mica material now. Dark is the chosen direction;
  a theme toggle is a future option.
- No changes to `engine`, `metrics`, `model`, or probing beyond the rolling-
  window size change already made (see below).
- No functional change to the bufferbloat flow, system tray, or font scaling.

## Related change already applied

`internal/engine/engine.go` `DefaultConfig().Window` was reduced `30 → 15`
(packet-loss / stats now recover over ~15s instead of ~30s). This is independent
of the redesign and already applied to the working tree.

## Design

### 1. Layout & navigation

Replace the six stacked collapsible sections with a **pinned summary + a custom
tab strip**:

```
┌───────────────────────────────────────────────┐
│  ● Agent Smith                          — □ ✕  │  title bar (dark)
├───────────────────────────────────────────────┤
│  ● OPERATIONAL   Your connection is healthy    │  status word + verdict  ┐
│  Good for gaming, calls and streaming.         │                          │ PINNED
│  ┌────────┐┌────────┐┌────────┐┌────────┐      │  4 stat tiles           │ (always
│  │LATENCY ││JITTER  ││LOSS    ││BUFFER  │      │                          │  visible)
│  └────────┘└────────┘└────────┘└────────┘      │                          │
│  ┌───────────── RTT chart ───────────────┐     │  time-series (fixed ht)  ┘
│  └────────────────────────────────────────┘    │
│  [ Path ] Connection  System  DNS  Events      │  custom painted tab strip
│  ┌────────────────────────────────────────┐    │
│  │  (active tab content — only one shown)  │    │  swappable content area
│  └────────────────────────────────────────┘    │
├───────────────────────────────────────────────┤
│  [ Run Bufferbloat Test ]        Grade A …     │  pinned action bar
└───────────────────────────────────────────────┘
```

- **Pinned (top):** status word + verdict line, the 4 stat tiles
  (Latency / Jitter / Loss / Bufferbloat), and the RTT chart at a fixed height.
- **Tabs:** `Path · Connection · System · DNS · Events`. Exactly one tab's
  content Composite is visible at a time; clicking a tab shows it and hides the
  others. The active tab gets an accent underline.
- **Pinned (bottom):** the action bar (Run Bufferbloat Test + status label).
  **"Clear Events" moves into the Events tab.**

### 2. Custom tab strip (not walk's native `TabWidget`)

**Decision:** do **not** use `walk`'s native `TabWidget`. Its tab headers are
native Win32 controls that will not honor the dark palette — they render as light
system tabs and reintroduce the "dated" look.

Instead, build a **custom painted tab strip** reusing the existing
`CustomWidget` + `DrawTextPixels` pattern already used for the collapsible
section headers (`paintHeader`/`OnMouseDown`). The strip is a horizontal row of
painted tab widgets; clicking one calls a `selectTab(i)` that sets visibility on
the five content Composites (mutually exclusive) and invalidates the strip so the
active-tab styling repaints. This is the same show/hide plumbing that exists
today for collapsible sections — just horizontal and single-select — so it is
low-risk and keeps full control of styling.

### 3. Visual system — "Refined Dark" tokens

Replace today's ad-hoc colors, margins, and font sizes with one consistent token
set.

**Palette** (retune the existing package-level color vars):

| Token        | Hex        | Use                                   |
|--------------|------------|---------------------------------------|
| bg           | `#0d0f12`  | window background                     |
| surface      | `#161a20`  | cards / tiles / panels                |
| surface2     | `#12151a`  | header bar, tab strip, track fills    |
| line         | `#272c34`  | hairlines / borders                   |
| text         | `#e6e8eb`  | primary text                          |
| sub          | `#8b929c`  | labels, muted text                    |
| accent       | `#4f8cff`  | active tab, focus, chart "Internet"   |
| good         | `#34d399`  | healthy status                        |
| amber        | `#fbbf24`  | watch/degraded status                 |
| red          | `#f87171`  | critical/fault status                 |

**Color rule:** neutral surfaces everywhere; saturated color used **only** for
status meaning (ratings, severity, functional chart series). Remove decorative
color that carries no status meaning.

**Spacing — one 8px grid:**

- Outer window margin: 16
- Gap between cards/tiles: 10
- Inner padding within cards/tiles: 8–12 (consistent per component type)
- Card/tile corner radius: 8

This single scale replaces the current mixed values (`mg(13,11,13,11)`,
spacings of 4/5/8/10, etc.) and is the primary fix for "weird spacing / not
lined up."

**Typography:**

- Segoe UI (Semibold, not heavy Bold) for labels, verdict, tab titles.
- Consolas retained for numeric values and table columns.
- Fixed size ramp: verdict 18, tile value 19, uppercase tile/column labels 9,
  body/table 11.

### 4. Implementation approach

All changes are within `internal/ui/gui/gui_windows.go`.

**Remove** the collapsible-section machinery:
- Fields: `secHdrW [6]`, `secContent [6]`, `secOpen [6]`, `secTitle [6]`.
- Functions: `secHeader` (the declarative builder), `paintHeader`,
  `toggleSection`.

**Add** the tab-strip machinery (mirrors the removed pattern):
- Fields: `tabHdrW [5]*walk.CustomWidget`, `tabContent [5]*walk.Composite`,
  `tabTitle [5]string`, `activeTab int`.
- Functions: `paintTab(i, canvas)` (painted tab with accent underline when
  active), `selectTab(i)` (set visibility on the five content Composites,
  invalidate the strip).

**Restructure** `Run()`'s `MainWindow.Children`:
- A pinned header Composite: status pill + verdict + tiles grid + RTT chart
  (`u.spark`) at a fixed `MinSize`/height.
- The custom tab strip Composite (row of 5 painted `CustomWidget`s).
- The five content Composites (Path grid, Connection grid, System bars, DNS
  grid, Events table+detail) — built from the *existing* child-widget slices
  (`ringChildren`, `connChildren`, `dnsChildren`, the `sysBars` custom widget,
  the issue table/detail). Only Path visible initially.
- The pinned action bar (move "Clear Events" into the Events content Composite).

**Unchanged:**
- `render()` and all its helpers keep targeting the same labels/widgets, which
  now live inside tab content Composites.
- `collectLabels` recursion still reaches every label (containers are walked
  recursively), so Ctrl+wheel font scaling keeps working.
- Tray setup, bufferbloat flow, issue model/detail, and all painting
  (`paintSpark`, `paintSysBars`) are untouched except palette-token retune.

## Risks & mitigations

- **Native tab styling clashing with dark theme** → mitigated by the custom
  painted tab strip (§2); we never instantiate a native `TabWidget`.
- **Pinned area too tall** (verdict + tiles + chart) leaving little room for tab
  content on small windows → give the RTT chart a modest fixed height and let
  the tab content Composite take the stretch factor; keep the window `MinSize`
  as-is (760×560).
- **Regression in font scaling / drill-down** → covered by keeping `render()`,
  `collectLabels`, and the issue model untouched; verify manually after the
  restructure.

## Verification

This is a Windows-only, GDI-painted native UI; there is no automated UI test
harness. Verification is a Windows build + manual pass:

1. `go build ./...` succeeds (build tag `//go:build windows`).
2. Existing non-GUI tests still pass (`go test ./...` for `metrics`,
   `interpret`, etc.) — unaffected, but run as a guard.
3. Manual checks: all five tabs switch on click with correct active-tab
   styling; pinned summary and action bar stay put; tray show/hide works;
   Ctrl+wheel still scales fonts; bufferbloat test runs and updates its tile;
   clicking an event row still shows detail; spacing/alignment visibly
   consistent across tabs.
