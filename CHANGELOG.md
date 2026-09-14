# Changelog

All notable changes to Agent Smith are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] — 2026-09-14

### Added — Internet performance monitoring
A single-vantage-point take on the commercial IPM feature set (Catchpoint /
LogicMonitor IPM): the same internet layers, measured from this PC.

- **Synthetic HTTP checks** (`internal/synth`) every 60 s with an `httptrace`
  breakdown (DNS, connect, TLS, TTFB, download, total), availability over a
  20-result window, and the CDN edge/PoP parsed from response headers
  (Cloudflare, Fastly, CloudFront, Akamai, Google). 18 presets: CDN (Cloudflare,
  Fastly, CloudFront, Akamai, Google Edge), Cloud (AWS S3 us-east-1, Azure, Google
  Cloud Storage, Oracle Cloud Ashburn), SaaS (Microsoft 365, Microsoft Teams,
  Google Workspace, Zoom, Slack, GitHub API, Steam Store), AI API (OpenAI,
  Anthropic). Custom checks via `config.json`.
- **Hop-by-hop path monitoring** (`internal/pathmon`, `internal/asn`): every anchor
  traced every 3 min with 3 probes per hop — per-hop loss, avg/max RTT, reverse
  DNS, ASN + AS name (Team Cymru DNS, cached), LAN/ISP/transit/destination
  segmentation, AS path, and **route-change detection**.
- **Topology-aware probable cause**: the traceroute is read with engineer's rules
  (loss or a ≥ 40 ms jump only counts if it persists downstream) to find the first
  degraded hop; an "upstream" verdict is re-attributed to the ISP or LAN when that
  hop lives there, with the hop, address and AS cited in the detail.
- **BGP visibility** (`internal/bgp`): hourly RIPEstat lookup of the connection's
  own prefix — announced?, origin AS, share of route collectors seeing it — shown
  in the Connection tab and noted by the classifier below 90 %.
- **Authoritative nameserver timing** (`dnsprobe.MeasureAuthoritative`): queries
  sent straight to the nameservers of google.com, cloudflare.com and github.com
  every 5 min, listed under the resolver comparison in the DNS tab.
- **7-day SLA / baselines** (`internal/baseline`): per-minute buckets retained for
  7 days; availability + p95 over 1 h / 24 h / 7 d for the internet, ISP-hop and
  gateway rings and every synthetic check; SLO compliance (default 99.9 % up,
  internet p95 ≤ 100 ms) with error-budget accounting.
- **Anomaly detection**: robust 24 h baseline (median / MAD); a value ≥ 4 robust
  standard deviations and ≥ 5 ms above your own normal raises a *Watch* verdict
  ("latency above your normal baseline") before anything fails.
- **Incidents / alert compression** (`internal/incident`): consecutive degraded
  verdicts are grouped into one incident (start, duration, peak severity, ticks,
  culprits seen); closes after 45 s healthy. Shown above the event log with a
  Clear Incidents button; **tray notifications** when an incident opens/resolves.
- **New verdicts**: *Remote service* (a check keeps failing while your path is
  healthy — "it's them, not you") and a captive-portal / proxy rule when every
  HTTP check fails while ping works.
- **User configuration** (`internal/config`): `%APPDATA%\AgentSmith\config.json`
  with extra anchors, synthetic categories / custom / disabled checks and cadence,
  path cadence and probes per hop, SLO, and notifications; a template is written
  on first run.
- **New GUI tabs**: **Route** (hop table + diagnosis + route changes), **Services**
  (check table with DNS/TLS/TTFB/total/availability/edge and a *Check now* button,
  also in the tray menu), **SLA** (availability / p95 / baseline / budget / SLO /
  anomaly per series).
- **CLI**: services, route, BGP, authoritative-DNS, SLA and incident sections in the
  live dashboard; new one-shot modes `--trace HOST` (enriched traceroute with the
  degraded-hop reading), `--check` (run all synthetic checks once) and `--report`
  (SLA / baseline / incident / route-change report from persisted history).

### Added — icon and splash screen
- **Agent Smith character icon** (face-centred, rounded, 16–256 px) for the
  exe, window, taskbar, tray and toasts, and as the header brand mark; a
  **splash screen** with the same still fills the window while the engine takes
  its first readings (at least 1.6 s, at most 6 s) and fades out once the path
  is discovered, showing the first verdict as it goes. The image is the
  *Agent Smith (The Matrix)* still hosted on Wikipedia as non-free content
  (© Warner Bros.); see the Credits note in the README.

### Changed — mobile-style UI
- **GUI rebuilt on Wails v2 / WebView2** as a phone-shaped frameless window
  (440×880, min 380×620) with five bottom tabs — Home · Services · Route ·
  Insights · Events — and bottom-sheet details: a per-service timing waterfall
  (DNS → connect → TLS → wait → download), hop detail, event drill-down and an
  incident timeline. The interim eight-tab walk layout is superseded.
- RTT chart with a crosshair tooltip and 5 m / 20 m / 1 h range chips; status ring
  showing the 24 h error budget; sparkline and baseline on the latency tile.
- Windows toast notifications (`beeep`) replace the tray balloons; the tray icon
  (`fyne.io/systray`) keeps show / run checks / bufferbloat / quit.
- Ctrl+wheel zoom (WebView2) replaces the custom font scaling.
- `lxn/walk` and `lxn/win` removed; the GUI build now needs
  `-tags desktop,production` (`scripts\build.ps1` and CI updated).

### Changed
- BGP prefix / visibility live in the Connection card; authoritative nameserver
  timing under Insights; the incident list and a Clear incidents button on Events.
- `state.json` is version 2 (adds baseline series, incidents, route changes);
  version-1 files load unchanged.
- The Microsoft Teams preset targets `https://teams.microsoft.com/favicon.ico`
  (the site root redirects in a loop for non-browser clients).
- Classifier verdicts are refined after the core rules with the path diagnosis,
  synthetic results, baseline anomaly and BGP visibility.

### Added
- **Connection panel** — public IP, ISP, plan/org, network **ASN**, geo-location,
  connection type, reverse DNS, and the **ISP support/repair phone number + outage
  page** (curated directory keyed by ASN/name; shown prominently for when the
  connection is down), via a geo-IP lookup at startup (refreshed hourly). New
  `internal/ispinfo` package.
- **Per-resolver DNS monitoring** — measures resolution latency against each
  resolver *directly* (system/configured, Cloudflare, Google, and the LAN gateway)
  so a slow configured resolver stands out. New DNS comparison section.

### Changed
- **Redesigned the dashboard in an ops/Grafana style** (chosen from 3 mockups in
  `docs/mockups/`): big stat tiles (latency/jitter/loss/bufferbloat), an
  area-filled RTT chart with scale labels and legend, the path table, and painted
  CPU/memory/GPU bars; dark title bar via DWM.
- **Reframed from "for gamers" to a general network & system performance monitor** —
  positioned for gaming, AI/ML workloads, streaming, video calls, and remote dev.
  Updated README, design brief, repo description, and all in-app text/verdicts.

### Added
- **Automatic active-adapter detection** via the OS routing table
  (`GetBestInterfaceEx`): correctly identifies the live egress interface (Wi-Fi or
  Ethernet) even when both are connected, instead of guessing the first with a gateway.
- **Total GPU, CPU and memory** in the resources view and in each recorded event.
- **Event list with drill-down** and a **Clear Events** button.

### Fixed
- Per-process CPU in event snapshots is normalized to a share of total system
  capacity (was reported per-core, so multi-core processes showed 600 %+).

### Added (earlier in this cycle)
- **Dark theme** for the GUI dashboard (Catppuccin Mocha palette).
- **Session RTT history sparkline** — a live chart of ping over time for the
  gateway, ISP hop, and internet rings (in-memory, current session only).
- **Mouse-over tooltips on every metric**, explaining what each value means and
  what's good/bad for gaming.
- Per-metric color coding (latency/jitter/loss/RSSI/bufferbloat grade) and an
  at-a-glance status pill.

## [0.1.0] — 2026-06-23

First public release: a working, gamer-focused connection-quality monitor for
Windows that measures the metrics that matter and localizes the bottleneck.

### Added
- **Monitoring engine** that probes concentric rings (default gateway → first ISP
  hop → public anchors) on a schedule and streams `Snapshot`s to the UIs.
- **Metrics core**: rolling RTT/loss windows with min/avg/p50/p95/p99, EWMA, and
  RFC 3550 interarrival jitter; gaming rating scales and DSLReports bufferbloat grades.
- **No-admin ICMP** via the Windows ICMP API (`IcmpSendEcho`), with a
  `golang.org/x/net/icmp` fallback on non-Windows for CI/dev.
- **Traceroute** with first-public-hop (ISP edge) discovery.
- **Local diagnostics**: default gateway, wired-vs-Wi-Fi detection, link speed, MTU,
  and NIC error/discard counters (IP Helper API); Wi-Fi RSSI/link-rate/SSID (WLAN API);
  CPU/memory/throughput and top processes (gopsutil); DNS resolution latency.
- **Bufferbloat tester**: idle-vs-loaded latency graded A+…F.
- **Bottleneck classifier**: most-local-first decision tree → plain-language verdict
  (Local machine | Wi-Fi | LAN/router | ISP access | Upstream internet | DNS) with a fix.
- **Native Windows GUI** (lxn/walk): dashboard window + system-tray icon + on-demand
  bufferbloat button; per-monitor DPI awareness.
- **CLI dashboard** (cross-platform) and a `--bufferbloat` one-shot mode.
- GitHub Actions CI (Windows + Linux, race tests) and a Windows GUI build artifact.

[Unreleased]: https://github.com/NYBaywatch/agent-smith/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/NYBaywatch/agent-smith/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/NYBaywatch/agent-smith/releases/tag/v0.1.0
