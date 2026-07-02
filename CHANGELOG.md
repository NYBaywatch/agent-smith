# Changelog

All notable changes to Agent Smith are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/NYBaywatch/agent-smith/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/NYBaywatch/agent-smith/releases/tag/v0.1.0
