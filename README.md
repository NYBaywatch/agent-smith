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
| **HTTP synthetics** | Per-endpoint DNS / connect / TLS / TTFB / total, availability, and which CDN edge answered — SaaS, cloud, CDN, AI APIs | TTFB < 200 ms, 100 % up |
| **Hop-by-hop path** | Per-hop loss / RTT / ASN to the internet, and the *first hop* where degradation really starts | clean path |
| **Authoritative DNS** | Time to answer straight from a domain's own nameservers — the cost of a resolver cache miss | < 100 ms |
| **BGP visibility** | How much of the internet's route collectors can see your own prefix | ≥ 90 % |
| **SLA / baseline** | Availability + p95 over 1 h / 24 h / 7 d, a 24 h baseline, and the SLO error budget left | within your SLO |

## How it localizes the problem

It probes **concentric rings** and compares them — your **gateway** (LAN), the **first
ISP hop**, and **public anchors** (`1.1.1.1`, `8.8.8.8`) — while watching local signals
(Wi-Fi RSSI, NIC link speed/errors, **CPU / memory / GPU** pressure, DNS latency). A
degradation that first appears at hop *N* and persists downstream is introduced at hop
*N*. The classifier turns those signals into a plain-language verdict and points you at
the **right fix** (Ethernet, router SQM for bufferbloat, Wi-Fi channel, NIC power
settings, DNS, or "close the job pegging your CPU") — never a black-box "booster."

## Internet performance monitoring

Commercial internet performance monitoring (IPM) products — Catchpoint /
LogicMonitor IPM, ThousandEyes — watch the "internet stack" between users and
apps (DNS, CDN, BGP, ISPs, SaaS) from thousands of global vantage points. Agent
Smith is a **single-vantage-point** take on the same idea: it measures the same
layers, but only from *this PC* and *this connection* — which is exactly the
view you need to answer "is it me, or is it them?"

- 🌐 **Synthetic HTTP checks** every 60 s against 18 built-in endpoints, with a
  DNS → connect → TLS → TTFB → download breakdown, availability, and the CDN
  edge/PoP that served each request:
  - *CDN* — Cloudflare, Fastly, CloudFront, Akamai, Google Edge
  - *Cloud* — AWS S3 (us-east-1), Azure, Google Cloud Storage, Oracle Cloud (Ashburn)
  - *SaaS* — Microsoft 365, Microsoft Teams, Google Workspace, Zoom, Slack, GitHub API, Steam Store
  - *AI API* — OpenAI API, Anthropic API
- ➕ **Custom checks** (your own app, a partner API, a game's login server) and
  extra ping anchors via `config.json` — see [Configuration](#configuration).
- 🛣️ **Hop-by-hop path visualization** — every anchor is traced every 3 min with
  3 probes per hop: per-hop loss, avg/max RTT, reverse DNS, ASN (Team Cymru), and
  LAN / ISP / transit / destination segmentation, plus **route-change detection**.
- 🎯 **Topology-aware probable cause** — the traceroute is read the way a network
  engineer reads it (loss that vanishes downstream is ICMP rate limiting, not real
  loss) to find the *first* hop that introduces persistent loss or latency, and the
  classifier re-attributes an "upstream" verdict to your ISP or LAN when that hop
  lives there.
- 🗺️ **BGP visibility** — hourly RIPEstat lookup of your own public prefix: is it
  announced, from which AS, and what share of route collectors see it.
- 🧭 **Authoritative nameserver timing** — queries sent straight to the
  nameservers of google.com, cloudflare.com and github.com every 5 min.
- 📈 **7-day SLA** — availability and p95 latency over 1 h / 24 h / 7 d for the
  internet, ISP-hop and gateway rings and every synthetic check, judged against a
  configurable SLO (default 99.9 % up, internet p95 ≤ 100 ms) with the **error
  budget** left.
- 🔍 **Baseline & anomaly detection** — a robust 24 h baseline (median / MAD) per
  series; a value ≥ 4 robust standard deviations above *your own normal* is flagged
  as a Watch before anything actually fails.
- 🚨 **Incidents (alert compression)** — consecutive degraded readings become one
  incident with start, duration, peak severity and tick count, instead of hundreds
  of events; tray notifications when an incident opens and when it resolves.
- 🙅 **"It's them, not you"** — a service whose synthetic check keeps failing while
  your gateway, ISP edge and internet anchors are healthy is called out as a
  *remote service* fault; every HTTP check failing while ping works is flagged as a
  captive portal / proxy / clock problem.

**What it deliberately does not do:** no global vantage points (every measurement
is from this PC — a remote service may look fine from elsewhere); no real-user
monitoring, Core Web Vitals or session replay (those need browser instrumentation);
no live BGP feed (RIPEstat is polled once an hour); no HTTP/3, ECN or MQTT probes.

## Features

- 📡 **Concentric-ring probing** with rolling min/avg/p50/p95/p99, EWMA, RFC 3550
  jitter and loss per target.
- 🧠 **Bottleneck classifier** — a most-local-first decision tree → plain-language
  verdict and recommended fix, refined by the hop-by-hop path reading, synthetic
  checks, your 24 h baseline and BGP visibility.
- 🌐 **Internet performance monitoring** — synthetic SaaS / cloud / CDN / AI-API
  checks, hop-by-hop route with ASNs, BGP prefix visibility, authoritative DNS,
  7-day SLA with baselines, anomaly detection and grouped incidents (see above).
- 🔌 **Automatic adapter detection** — uses the OS's own routing to identify the live
  egress interface (Wi-Fi *or* Ethernet) even when both are connected.
- 📊 **System resources** — total CPU, total memory (used/total), and **total GPU**
  utilization (Windows PDH), so machine-side bottlenecks are caught too.
- 🧪 **On-demand bufferbloat test** — saturates the link and grades added latency (A+…F).
- 🗂️ **Event log with drill-down** — every detected problem is recorded with a timestamp,
  the exact degraded metrics, system state, and a `ps`-style snapshot of the busiest
  processes; **persists across sessions**, alongside incidents and 7 days of baselines.
- 🖥️ **Native tray app** (lxn/walk): dark dashboard with a live RTT history sparkline,
  tabs for **Path · Route · Services · SLA · Connection · System · DNS · Events**,
  tooltips on every metric, and Ctrl+wheel font scaling — plus a cross-platform CLI
  dashboard and `--bufferbloat` / `--trace` / `--check` / `--report` one-shot modes.

## Architecture

```
                       ┌──────────────────────────── engine ───────────────────────────┐
  probe (ICMP API) ───▶│  schedules concentric-ring probes (gateway/ISP/internet)        │
  netinfo (iphlpapi) ─▶│  auto-detects active adapter + Wi-Fi/NIC                         │──▶ model.Snapshot ──▶ classifier ──▶ Verdict
  sysinfo (gopsutil  ─▶│  samples CPU / memory / GPU / throughput                         │            │
   + PDH GPU) │         │  measures DNS latency (+ authoritative NS); records history      │            ├──▶ ui/gui (walk: window + tray;
  bufferbloat ────────▶│  on-demand latency-under-load grade                              │            │      Path · Route · Services · SLA ·
  synth (httptrace) ──▶│  synthetic SaaS/cloud/CDN/AI-API checks every 60 s               │            │      Connection · System · DNS · Events)
  pathmon + asn ──────▶│  hop-by-hop traceroute + ASN + route changes every 3 min          │            └──▶ ui/cli (live dashboard)
  bgp (RIPEstat) ─────▶│  own-prefix visibility hourly                                    │
  baseline ───────────▶│  7-day per-minute buckets → SLA / baseline / anomaly             │
  incident ───────────▶│  groups degraded ticks into incidents (alert compression)        │
  config ─────────────▶│  config.json: anchors, checks, SLO, cadences  (state persisted)  │
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
# One-shot modes:
go run ./cmd/agent-smith --bufferbloat      # saturate the link, grade latency under load
go run ./cmd/agent-smith --trace 1.1.1.1    # enriched traceroute: loss, RTT, rDNS, ASN, degraded-hop reading
go run ./cmd/agent-smith --check            # run every synthetic HTTP check once, print timings
go run ./cmd/agent-smith --report           # SLA / baseline / incident report from persisted history
```

Pre-built Windows binaries are attached to each [release](https://github.com/NYBaywatch/agent-smith/releases).

### Configuration

Agent Smith writes a template to `%APPDATA%\AgentSmith\config.json` on first run
(`~/.config/AgentSmith/config.json` elsewhere). Edit it and restart the app:

```json
{
  "version": 1,
  "anchors": [
    { "name": "Valorant NA-East", "host": "192.0.2.10" }
  ],
  "synthetics": {
    "enabled": true,
    "interval_seconds": 60,
    "categories": ["SaaS", "Cloud", "CDN", "AI API"],
    "custom": [
      { "name": "My app", "url": "https://app.example.com/healthz", "expect_status": 200 },
      { "name": "Partner API", "url": "https://api.partner.example/v1/ping", "category": "Custom" }
    ],
    "disabled": ["Steam Store"]
  },
  "path": { "enabled": true, "interval_seconds": 180, "probes_per_hop": 3 },
  "slo": { "availability": 0.999, "p95_ms": 100 },
  "notifications": true
}
```

- `anchors` — extra ICMP targets for the internet ring (IPs; hostnames are
  resolved once at startup).
- `synthetics.categories` — which built-in preset groups run; `disabled` lists
  preset names or URLs to skip; `custom` adds your own checks (`expect_status`
  0 = any status below 500 counts as up, so a 401 from an API still proves
  reachability). `interval_seconds` floor is 15.
- `path` — hop-by-hop tracing cadence (floor 30 s) and probes per hop.
- `slo` — the objective the SLA tab and `--report` judge against; `p95_ms` applies
  to the internet ring only (0 disables the latency objective).
- `notifications` — tray balloons when an incident opens / resolves.

Runtime state (RTT history, events, incidents, 7 days of baselines) lives next to
it in `state.json`.

## License

MIT — see [LICENSE](LICENSE).
