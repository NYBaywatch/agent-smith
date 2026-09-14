# Agent Smith — Mobile-style UI on Wails

**Date:** 2026-09-14
**Scope:** replace the lxn/walk GUI with a WebView2 (Wails v2) front end; engine untouched
**Status:** designed, implementing

## Why

The owner asked for a revamp that feels like a phone app. Real Win32 controls
(walk) cannot do rounded cards, a bottom tab bar, bottom sheets or smooth
transitions without hand-painting everything. Wails v2 embeds the page in
WebView2 (already on every Windows 11 machine), needs no C compiler, and ships
as one exe. The front end is vanilla HTML/CSS/JS with no bundler so CI stays
Go-only.

## Concept

A tall, narrow window that reads like a phone screen on the desktop:

```
┌──────────────────────────────┐
│ ◈ Agent Smith        – □ ×   │  slim drag-handle header (frameless window)
├──────────────────────────────┤
│  ● OPERATIONAL               │  hero: status ring + headline + fix
│  Connection is healthy        │
│ ┌────────┐ ┌────────┐        │  2×2 metric tiles with sparklines
│ │LATENCY │ │JITTER  │        │
│ │ 12 ms  │ │ 1.4 ms │        │
│ └────────┘ └────────┘        │
│ ┌──────── RTT · 20 min ─────┐│  area chart, crosshair tooltip
│ └───────────────────────────┘│
│ ┌ Path ────────────────────┐ │  vertical hop ladder LAN → ISP → NET
│ ┌ System ──────────────────┐ │  meters
│ ┌ Connection ──────────────┐ │
│          (scrolls)           │
├──────────────────────────────┤
│  ⌂ Home  ◎ Services  ⟶ Route │  bottom tab bar, 5 destinations
│  ▤ Insights  ⚑ Events        │
└──────────────────────────────┘
```

- **Screens** (bottom tabs): Home · Services · Route · Insights · Events.
  Each screen is a vertical feed of cards; exactly one screen is mounted.
- **Bottom sheets** for detail: tap a service → timing waterfall (DNS →
  connect → TLS → TTFB → download) and history; tap a hop → hop detail; tap an
  event → the interpreted measurements and process snapshot; tap an incident →
  its timeline.
- **Actions** live where they belong: "Check now" on Services, "Trace now" on
  Route, "Bufferbloat test" on Home (hero), "Clear" on Events.
- **Window:** frameless, default 440×880, min 380×620, resizable; the header is
  the drag region with minimise / close (close hides to tray). Ctrl+wheel zoom
  is left enabled in WebView2 (replaces the old font-scaling).
- **Tray:** fyne.io/systray (pure Go) — show, run checks, bufferbloat, quit.
- **Notifications:** Windows toasts via beeep when an incident opens / resolves.

## Tokens

Dark only, by choice: an always-on monitor that sits beside games and terminals.

| Token | Value | Use |
|---|---|---|
| `--bg` | `#0e1116` | page |
| `--surface` | `#171b22` | cards, sheets, tab bar |
| `--surface-2` | `#1f242d` | meters' tracks, nested chips |
| `--line` | `rgba(255,255,255,.08)` | hairlines |
| `--text` | `#eef1f5` | primary ink |
| `--text-2` | `#a9b1bd` | secondary ink |
| `--muted` | `#6f7885` | captions, axis |
| `--accent` | `#b69dff` | active tab, primary button, focus ring |
| `--good` / `--warn` / `--bad` | `#31c46e` / `#f2b53a` / `#ef5b5b` | status only, always with a label |
| series NET / ISP / LAN | `#3987e5` / `#d95926` / `#199e70` | chart identity — validated (CVD ΔE ≥ 9.4, normal ≥ 26.5, ≥ 3:1) |

Type: `"Segoe UI Variable Display"` for the hero and card titles, `"Segoe UI
Variable Text", "Segoe UI"` for body, `"Cascadia Mono", Consolas` for numbers
in columns (`tabular-nums`). Scale: hero 40, tile value 28, title 15/600,
body 13, caption 11 uppercase +0.08em.

Spacing: 16 px gutter, 12 px between cards, 14 px card padding, radius 18 px
(cards) / 24 px (sheets) / 999 (pills). Bottom bar 64 px. Motion: 160 ms
crossfade between screens, 220 ms sheet slide, none under
`prefers-reduced-motion`.

## Data contract (Go ⇄ JS)

Bound struct `web.App`, reachable as `window.go.web.App.<Method>()`. All JSON
keys are snake_case; durations are milliseconds as numbers; times are RFC 3339.

- `Snapshot()` → the DTO below; the same object is pushed on every engine tick
  as event `"snapshot"`.
- `History()` → `[{t, gw, isp, net}]`, ms, oldest first, downsampled to ≤ 600
  points.
- `Issues()` → newest first: `{time, severity, severity_name, culprit,
  headline, detail, fix, summary, lines:[{label, value, rating, meaning}],
  procs:[{pid, name, cpu, mem_mb}]}`.
- `Incidents()` → newest first: `{id, start, end, open, duration_s, culprit,
  peak, peak_name, ticks, headline, last, fix, culprits:[...]}`.
- `Info()` → `{version, config_path, state_path, started}`.
- Actions: `RunBufferbloat()` → `{grade, added_ms, idle_ms, loaded_ms,
  down_mbps, error}`; `RunChecks()` → services array; `Trace(host)` → path;
  `ClearIssues()`, `ClearIncidents()`, `Minimise()`, `Hide()`, `Quit()`,
  `OpenURL(url)`.
- Event `"incident"` → `{opened, closed}` (each an incident or null).

Snapshot DTO:

```
time, verdict{culprit, severity, severity_name, confidence, headline, detail, fix}
rings[{ring: LAN|ISP|NET, name, host, alive, mean_ms, p95_ms, jitter_ms, loss_pct, rating}]
headline{latency_ms, latency_rating, jitter_ms, jitter_rating, loss_pct, loss_rating,
         bufferbloat{grade, added_ms, idle_ms, loaded_ms, down_mbps} | null}
sys{cpu_pct, mem_pct, mem_used_gb, mem_total_gb, gpu_pct(-1 = n/a), in_mbps, out_mbps}
net{interface, media, link_mbps, mtu, errors, gateway, wifi{ssid, rssi, quality, rx_mbps, tx_mbps}|null} | null
dns{avg_ms, slow, lookups, failed, servers[{name, addr, avg_ms, ok, slow}],
    authoritative[{domain, ns, addr, ms, ok, err}]}
connection{ip, isp, org, asn, as_name, location, type, reverse, support, support_url} | null
bgp{prefix, origin_asn, visibility_pct, visible_peers, total_peers, announced, healthy, fetched_at} | null
services[{name, category, provider, url, status: ok|slow|fail|down|pending, dns_ms, connect_ms,
          tls_ms, ttfb_ms, download_ms, total_ms, avail_pct, runs, edge, error, http_status, last_at}]
paths[{name, dest, when, reached, as_path, elapsed_ms,
       hops[{ttl, addr, host, asn, as_name, avg_ms, max_ms, loss_pct, private, segment, degraded}],
       diagnosis{hop_index, reason, segment}}]
route_changes[{when, name, dest, hops_before, hops_after, as_before, as_after}]
sla[{key, name, kind, avail_1h_pct, avail_24h_pct, avail_7d_pct, p95_24h_ms, mean_24h_ms, samples_24h,
     baseline_ms, baseline_valid, budget_left_pct, slo_ok, availability_ok, latency_ok, now_ms, z, anomalous}]
slo{availability_pct, p95_ms}
incident{id, start, duration_s, culprit, peak, peak_name, ticks, headline} | null
alerts{raw, incidents}
```

Ratings are the words `Excellent | Good | Fair | Poor | —`.
