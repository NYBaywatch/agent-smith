# Agent Smith — Design Brief

> *"Never send a human to do a machine's job."* — Agent Smith
>
> **Agent Smith** is a native Windows desktop agent that continuously watches your
> internet connection the way a competitive gamer cares about it — latency, jitter,
> packet loss, and bufferbloat — and then **hunts down where the problem actually
> lives**: your PC, your Wi‑Fi, your router/LAN, your ISP access link, or the wider
> internet. Generic speed tests tell you a number. Agent Smith tells you *who to blame.*

---

## 1. Product vision

Most "internet quality" tools optimize for the wrong metric for gamers: **throughput**.
A 1 Gbps connection that adds 300 ms of latency under load is *worse* for Rocket League,
Valorant, or Street Fighter than a 50 Mbps line that stays flat. Agent Smith is built
around the metrics that actually decide whether a game feels good, and around **fault
localization** — the single most useful thing a monitor can do is answer *"is it me or
is it them?"*

Design principles:

1. **Latency-first, not bandwidth-first.** Throughput is a context metric, not the headline.
2. **Localize, don't just measure.** Every sample feeds a classifier that names the most
   likely bottleneck segment.
3. **No admin required for the core loop.** Use the Windows ICMP API, not raw sockets.
4. **No snake oil.** We measure and explain; we don't claim magic "boosters."
5. **UI-agnostic engine.** A pure-Go engine drives both a headless CLI dashboard
   (CI-testable, no display) and a native Win32 GUI (lxn/walk).

---

## 2. Metrics that matter for gaming

| Metric | Excellent | Good | Playable | Poor | How we measure |
|---|---|---|---|---|---|
| **Latency (RTT)** to game-relevant host | < 20 ms | 20–50 ms | 50–100 ms | > 100 ms | ICMP echo to reference + (optional) game-server hosts |
| **Jitter** (RTT variation) | < 5 ms | 5–15 ms | 15–30 ms | > 30 ms | RFC 3550 interarrival jitter + stddev of RTT window |
| **Packet loss** | < 0.1 % | 0.1–1 % | 1–2.5 % | > 2.5 % | lost/sent over rolling probe window |
| **Bufferbloat** (added latency under load) | A/A+ | B | C | D/F | idle RTT vs RTT during saturating transfer |
| **Throughput** | context only | — | — | — | gaming needs < 1 Mbps; we track it to detect *contention*, not as a score |

### Bufferbloat grading (Waveform / DSLReports scale)

Bufferbloat is the latency that piles up when buffers fill under load. It is the #1
cause of "my ping is fine but I lag when someone streams Netflix." We grade the
**increase** in latency under load vs idle baseline:

| Grade | Added latency under load (DSLReports A–F scale) |
|---|---|
| **A+** | 0–5 ms |
| **A** | 5–20 ms |
| **B** | 20–50 ms |
| **C** | 50–100 ms |
| **D** | 100–300 ms |
| **F** | > 300 ms |

A or B is fine for gaming; C and below means the access link (or its queue management)
is the problem and is fixable with SQM/QoS — Agent Smith flags this explicitly.

### Why jitter (RFC 3550)

Jitter is more disruptive than steady high ping for real-time games (it defeats client
prediction and causes rubber-banding). We compute the RFC 3550 interarrival jitter
estimate `J = J + (|D| - J)/16` alongside the simpler windowed standard deviation, and
report both p95 latency and jitter.

---

## 3. Bottleneck localization model

The core idea: probe **concentric rings** and compare them.

```
[ Your PC ] --LAN--> [ Router/GW ] --access--> [ ISP edge ] --core--> [ Internet ]
     |                    |                          |                      |
  local res.          gateway RTT               first-hop RTT          internet RTT
  (CPU/NIC/Wi-Fi)     (ping 192.168.x.1)        (traceroute hop 2)     (ping 1.1.1.1 / 8.8.8.8)
```

Signals collected:

- **Gateway RTT** — ICMP to the default gateway. Wired LAN should be < 2 ms; Wi‑Fi < 10 ms.
  High & jittery gateway RTT on Wi‑Fi ⇒ wireless problem; high on wired ⇒ cabling/switch/NIC.
- **First ISP-hop RTT** — second traceroute hop (the ISP access link / CMTS / DSLAM / OLT).
- **Internet RTT** — ICMP to anycast resolvers (1.1.1.1, 8.8.8.8) and optionally a game host.
- **Wi‑Fi quality** (`wlanapi`): RSSI (dBm), signal quality (%), PHY/link rate, band, SSID.
- **Wired vs wireless** (interface media type via `GetIfTable2`).
- **Interface counters** (`GetIfEntry2`): in/out errors, discards, link speed, utilization.
- **DNS resolution latency** — time to resolve a set of domains.
- **Local resource contention** (`gopsutil`): CPU %, memory pressure, total NIC throughput,
  top bandwidth/CPU processes (a 100 % CPU spike or a saturating upload skews everything).
- **MTU / errors** — interface MTU and rising error/discard counters.

### Wi‑Fi RSSI reference

| RSSI (dBm) | Quality | Gaming verdict |
|---|---|---|
| ≥ −50 | Excellent | Ideal |
| −50…−60 | Very good | Great |
| −60…−67 | Good | Minimum for stable gaming/voice |
| −67…−70 | Marginal | Expect occasional spikes |
| −70…−80 | Poor | Frequent jitter/loss |
| < −80 | Unusable | Move closer / use Ethernet |

Windows `wlanSignalQuality` is 0–100; we convert to dBm via `dBm = quality/2 − 100`.

---

## 4. Bottleneck classifier (decision heuristic)

Given a snapshot `{gwRTT, gwLoss, ispRTT, netRTT, netLoss, jitter, bloatGrade, wifi{rssi,linkMbps,isWifi}, cpu%, nicErrRate, nicUtil%, dnsMs}` the classifier emits a ranked culprit:

```
Culprit ∈ { Healthy, LocalMachine, WiFi, LAN_Router, ISP_AccessLink, UpstreamInternet, DNS }

1. LocalMachine   if cpu% > 90 (sustained) OR nicErrRate high OR nicUtil% > 90 (self-induced load)
                  → "Your PC is the bottleneck (CPU/NIC saturation), not the network."
2. WiFi           if isWifi AND (rssi < -70 OR linkMbps low OR gwRTT high & jittery)
                  → "Weak/!congested Wi-Fi between you and the router."
3. LAN_Router     if gwRTT or gwLoss high while wired (rssi n/a) 
                  → "Local network/router/cable problem."
4. ISP_AccessLink if gwRTT good BUT (bloatGrade ≤ C under load) OR ispRTT jump large
                  → "Your ISP access link / its queue (bufferbloat) is the problem."
5. UpstreamInternet if gw & ISP-hop good but internet RTT/loss bad
                  → "Problem is upstream / at the game server's path, not your LAN."
6. DNS            if dnsMs high but RTTs fine
                  → "Name resolution is slow; try a faster resolver (1.1.1.1/8.8.8.8)."
7. Healthy        otherwise.
```

Each rule carries a confidence score and a human-readable explanation + suggested fix.
Rules are evaluated most-local-first because a local fault masks everything downstream.
Full numeric thresholds live in `internal/classifier` and are unit-tested.

---

## 5. Measurement methodology on Windows

- **Pinging without admin:** raw ICMP sockets require Administrator on Windows. Agent Smith
  instead calls the **Windows ICMP API** (`IcmpCreateFile` / `IcmpSendEcho2` in `iphlpapi.dll`),
  which works for standard users and returns RTT + status codes. This is the canonical
  Windows approach (what `ping.exe` uses).
- **Traceroute:** repeated `IcmpSendEcho` with increasing IP TTL (`IP_OPTION_INFORMATION.Ttl`);
  `IP_TTL_EXPIRED_TRANSIT (11013)` replies reveal each hop's address and RTT.
- **RTT statistics:** rolling window per target → min / avg / p50 / p95 / p99 / max, EWMA of
  the mean, stddev, RFC 3550 jitter, loss %. Default cadence: one probe/target every 1 s,
  windows of 30–60 samples.
- **Bufferbloat test:** measure idle RTT baseline, then saturate the link (HTTP download from a
  CDN, optional upload) while continuously pinging; grade the latency delta on the A+…F scale.
- **ICMP caveat:** routers may deprioritize/limit ICMP; we treat loss to a single host
  cautiously and corroborate across multiple anycast targets before declaring loss.

---

## 6. Technology stack

| Concern | Choice | Import path |
|---|---|---|
| Language | Go 1.26 | — |
| Syscalls | `golang.org/x/sys/windows` | ICMP API, wlanapi, iphlpapi |
| System/Net stats | gopsutil v4 | `github.com/shirou/gopsutil/v4` |
| Native Win32 GUI | **lxn/walk** (native widgets, no CGO, Windows-only — matches "native Windows app") | `github.com/lxn/walk` |
| Tray / window | walk `NotifyIcon` + `MainWindow` | — |
| CLI dashboard | stdlib + ANSI | — |
| Manifest/resources | `github.com/tc-hib/go-winres` (DPI + common controls v6) | build-time |

**GUI choice rationale:** walk renders *real* Win32 controls (native look, tiny binary, no
bundled browser/OpenGL), which is exactly right for a lightweight always-on system-tray
monitor. Fyne/Wails/Gio are cross-platform but heavier (OpenGL or an embedded webview) and
less "native Windows." Since the brief is explicitly a *native Windows app*, walk wins.

Windows APIs called directly: `IcmpCreateFile`/`IcmpSendEcho2`, `GetAdaptersAddresses`,
`GetIfTable2`/`GetIfEntry2`, `WlanOpenHandle`/`WlanEnumInterfaces`/`WlanQueryInterface`.

---

## 7. Architecture & roadmap

```
cmd/agent-smith        entrypoint; --cli | --gui (default GUI on Windows), flags
internal/metrics       Sample, Window stats (percentiles, EWMA, RFC3550 jitter), grading
internal/probe         ICMP ping (Windows API) + cross-platform fallback, traceroute
internal/netinfo       default gateway, interfaces (media/wired-wireless/errors), Wi-Fi RSSI
internal/sysinfo       CPU/mem/net throughput, top processes (gopsutil)
internal/dnsprobe      DNS resolution latency
internal/bufferbloat   load-generating bufferbloat grader
internal/classifier    bottleneck decision tree (+ tests)
internal/engine        orchestrates probes on a schedule → Snapshot stream
internal/ui/cli        live terminal dashboard
internal/ui/gui        walk tray + dashboard window (build tag windows)
docs/DESIGN.md         this document
```

**MVP (v0.1):** engine + ICMP ring probes (gateway/internet) + jitter/loss/percentiles +
classifier + CLI dashboard. Cross-platform-buildable core, Windows ICMP backend.

**v0.2:** Wi‑Fi RSSI, interface stats, DNS latency, system contention, traceroute,
richer classifier confidence.

**v0.3:** native walk GUI (tray + dashboard, live sparklines), on-demand bufferbloat test,
rolling history persistence, desktop alerts when a segment degrades.

**v1.0:** signed release exe, auto-start option, configurable targets (add your game's
servers), exportable reports.

---

## 8. Risks & anti-features

- **Per-process bandwidth on Windows** is not reliably exposed without ETW/WinPcap; v0.x
  reports *system* throughput + top CPU processes and is honest about the limitation rather
  than faking per-app numbers.
- **ICMP ≠ game traffic.** ICMP is a proxy; we corroborate across targets and clearly label
  it as a reachability/latency probe, not a guarantee of game-port behavior.
- **No "boosters."** We will not claim to "optimize" routing we don't control. We diagnose
  and recommend real fixes (Ethernet, SQM/QoS for bufferbloat, channel changes, driver/power
  settings).
- **Admin:** core loop runs unprivileged. Some adapter detail may be richer with admin; we
  degrade gracefully and never *require* elevation.
