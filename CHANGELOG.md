# Changelog

All notable changes to Agent Smith are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres
to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
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
