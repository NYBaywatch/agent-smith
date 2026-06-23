# Agent Smith 🕶️

**A native Windows connection-quality agent for gamers — that hunts down *where* your lag actually lives.**

> *"Me, me, me..."* Generic speed tests answer the wrong question (*how many Mbps?*).
> Gamers care about a different physics: small, time-critical packets reaching the
> server with **low latency, low jitter, and near-zero loss — even while the link is
> busy.** Agent Smith measures exactly that, continuously, and then **localizes** the
> bottleneck to one of five culprits:

> **🖥️ Local machine · 📶 Wi-Fi · 🔌 LAN / router · 🛰️ ISP access link · 🌐 Upstream internet**

[![CI](https://github.com/NYBaywatch/agent-smith/actions/workflows/ci.yml/badge.svg)](https://github.com/NYBaywatch/agent-smith/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/NYBaywatch/agent-smith)](https://goreportcard.com/report/github.com/NYBaywatch/agent-smith)
![Platform](https://img.shields.io/badge/platform-Windows-0078D6)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8)
![License](https://img.shields.io/badge/license-MIT-green)

---

## Why?

A 1 Gbps connection that adds 300 ms of latency under load is **worse** for Valorant,
Rocket League, or a fighting game than a flat 50 Mbps line. Agent Smith is built around
the metrics that decide whether a game *feels* good, and around the one question a monitor
should actually answer: **"is it me, or is it them?"**

## What it measures

| Metric | Why it matters | Target for gaming |
|---|---|---|
| **Latency (RTT)** | Dominant factor in responsiveness | < 50 ms (competitive < 30 ms) |
| **Jitter** (RFC 3550) | Erratic timing defeats client prediction → rubber-banding | < 5 ms |
| **Packet loss** | A lost UDP packet is a lost world-update | < 1 % |
| **Bufferbloat** (latency under load) | "Fine until someone streams Netflix" | Grade A/B (≤ 50 ms added) |
| **Throughput** | A *precondition*, never the headline | a few Mbps is plenty |

## How it localizes the problem

It probes **concentric rings** and compares them — your **gateway** (LAN), the **first
ISP hop**, and **public anchors** (`1.1.1.1`, `8.8.8.8`) — while watching local signals
(Wi-Fi RSSI, NIC link speed/errors, CPU/bandwidth contention, DNS latency). A degradation
that first appears at hop *N* and persists downstream is introduced at hop *N*. The
classifier turns those signals into a plain-language verdict and points you at the **right
fix** (Ethernet, router SQM for bufferbloat, Wi-Fi channel, NIC power settings, DNS) —
never a black-box "booster."

## Design highlights

- **No admin required.** Uses the **Windows ICMP API** (`IcmpSendEcho`), not raw sockets.
- **Native Windows UI** via [lxn/walk](https://github.com/lxn/walk) — real Win32 widgets,
  tiny binary, system-tray friendly. (A cross-platform CLI dashboard ships too.)
- **UI-agnostic engine** in pure Go; the CLI is fully testable in CI without a display.
- **Honest.** No telemetry, no snake oil. We diagnose and explain.

## Status

🚧 Early development. See [`docs/DESIGN.md`](docs/DESIGN.md) for the architecture and
[`docs/RESEARCH.md`](docs/RESEARCH.md) for the multi-source research brief behind it.

## Build

```sh
go build ./...
go test ./...
# Windows GUI build (no console window):
go build -ldflags="-H windowsgui" -o agent-smith.exe ./cmd/agent-smith
# Headless live dashboard (any platform):
go run ./cmd/agent-smith --cli
```

## License

MIT — see [LICENSE](LICENSE).
