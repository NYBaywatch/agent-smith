# Agent Smith 🕶️

**A native Windows network & system performance monitor that hunts down *where* your bottleneck actually lives.**

> Generic speed tests answer the wrong question (*how many Mbps?*). What actually
> determines whether a real-time workload feels good is a different physics: small,
> time-critical packets reaching the far end with **low latency, low jitter, and
> near-zero loss — even while the link and machine are busy.** Agent Smith measures
> exactly that, continuously, alongside local CPU / memory / GPU pressure, and then
> **localizes** the bottleneck to one of five places:

> **🖥️ Local machine · 📶 Wi-Fi · 🔌 LAN / router · 🛰️ ISP access link · 🌐 Upstream internet**

[![CI](https://github.com/NYBaywatch/agent-smith/actions/workflows/ci.yml/badge.svg)](https://github.com/NYBaywatch/agent-smith/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/NYBaywatch/agent-smith)](https://goreportcard.com/report/github.com/NYBaywatch/agent-smith)
![Platform](https://img.shields.io/badge/platform-Windows-0078D6)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8)
![License](https://img.shields.io/badge/license-MIT-green)

---

## Who it's for

Anyone whose work or play depends on a connection (and a machine) that stays
responsive *under load*:

- 🎮 **Gaming** — latency, jitter and loss decide hit registration and rubber-banding.
- 🤖 **AI / ML workloads** — distributed training and inference are sensitive to RTT,
  jitter and loss; large dataset / model-weight transfers and API calls to hosted
  models care about throughput, DNS, and bufferbloat.
- 🎥 **Video calls & live streaming** — jitter and loss cause freezes and artifacts.
- 🖥️ **Remote dev / SSH / RDP / cloud** — every keystroke round-trips.

Agent Smith is built around the metrics that decide whether these feel good, and
around the one question a monitor should actually answer: **"is it me, or is it them?"**

## What it measures

| Metric | Why it matters | Healthy target |
|---|---|---|
| **Latency (RTT)** | Dominant factor in responsiveness | < 50 ms (real-time: < 30 ms) |
| **Jitter** (RFC 3550) | Erratic timing breaks prediction → stutter, rubber-banding, uneven throughput | < 5 ms |
| **Packet loss** | A lost packet is a lost update / a retransmit | < 1 % |
| **Bufferbloat** (latency under load) | "Fine until something else uses the link" (a download, a backup, a training job) | Grade A/B (≤ 50 ms added) |
| **Local CPU / memory / GPU** | A saturated machine *looks* like network lag and throttles workloads | headroom to spare |
| **Throughput** | A *precondition* (and it matters for bulk transfers), not the headline for latency | enough for the task |

## How it localizes the problem

It probes **concentric rings** and compares them — your **gateway** (LAN), the **first
ISP hop**, and **public anchors** (`1.1.1.1`, `8.8.8.8`) — while watching local signals
(Wi-Fi RSSI, NIC link speed/errors, **CPU / memory / GPU** pressure, DNS latency). A
degradation that first appears at hop *N* and persists downstream is introduced at hop
*N*. The classifier turns those signals into a plain-language verdict and points you at
the **right fix** (Ethernet, router SQM for bufferbloat, Wi-Fi channel, NIC power
settings, DNS, or "close the job pegging your CPU") — never a black-box "booster."

## Features

- 📡 **Concentric-ring probing** with rolling min/avg/p50/p95/p99, EWMA, RFC 3550
  jitter and loss per target.
- 🧠 **Bottleneck classifier** — a most-local-first decision tree → plain-language
  verdict and recommended fix.
- 🔌 **Automatic adapter detection** — uses the OS's own routing to identify the live
  egress interface (Wi-Fi *or* Ethernet) even when both are connected.
- 📊 **System resources** — total CPU, total memory (used/total), and **total GPU**
  utilization (Windows PDH), so machine-side bottlenecks are caught too.
- 🧪 **On-demand bufferbloat test** — saturates the link and grades added latency (A+…F).
- 🗂️ **Event log with drill-down** — every detected problem is recorded with a timestamp,
  the exact degraded metrics, system state, and a `ps`-style snapshot of the busiest
  processes; **persists across sessions**.
- 🖥️ **Native tray app** (lxn/walk): dark dashboard with a live RTT history sparkline,
  tooltips on every metric, and Ctrl+wheel font scaling — plus a cross-platform CLI
  dashboard and a `--bufferbloat` one-shot mode.

## Architecture

```
                       ┌──────────────────────────── engine ───────────────────────────┐
  probe (ICMP API) ───▶│  schedules concentric-ring probes (gateway/ISP/internet)        │
  netinfo (iphlpapi) ─▶│  auto-detects active adapter + Wi-Fi/NIC                         │──▶ model.Snapshot ──▶ classifier ──▶ Verdict
  sysinfo (gopsutil  ─▶│  samples CPU / memory / GPU / throughput                         │            │
   + PDH GPU) │         │  measures DNS latency; records history + issues (persisted)      │            ├──▶ ui/gui (walk: window + tray)
  bufferbloat ────────▶│  on-demand latency-under-load grade                              │            └──▶ ui/cli (live dashboard)
                       └─────────────────────────────────────────────────────────────────┘
```

The engine is UI-agnostic and emits a `Snapshot` stream; the classifier is a pure,
unit-tested function. See [`docs/DESIGN.md`](docs/DESIGN.md) for the full design and
[`docs/RESEARCH.md`](docs/RESEARCH.md) for the multi-source research brief behind it.

## Design highlights

- **No admin required.** Uses the **Windows ICMP API** (`IcmpSendEcho`), not raw sockets.
- **Native Windows UI** via [lxn/walk](https://github.com/lxn/walk) — real Win32 widgets,
  tiny binary, system-tray friendly, dark themed.
- **UI-agnostic engine** in pure Go; the CLI is fully testable in CI without a display.
- **Honest.** No telemetry, no snake oil. It diagnoses and explains.

## Build & run

```sh
go build ./...
go test ./...
# Windows GUI build (no console window):
go build -ldflags="-H windowsgui" -o agent-smith.exe ./cmd/agent-smith
# Headless live dashboard (any platform):
go run ./cmd/agent-smith --cli
# One-shot bufferbloat test:
go run ./cmd/agent-smith --bufferbloat
```

Pre-built Windows binaries are attached to each [release](https://github.com/NYBaywatch/agent-smith/releases).

## License

MIT — see [LICENSE](LICENSE).
