// Package model holds the shared data types exchanged between the engine, the
// bottleneck classifier, and the user interfaces. Keeping them here avoids an
// import cycle (engine → classifier → model ← ui).
package model

import (
	"time"

	"github.com/NYBaywatch/agent-smith/internal/baseline"
	"github.com/NYBaywatch/agent-smith/internal/bgp"
	"github.com/NYBaywatch/agent-smith/internal/bufferbloat"
	"github.com/NYBaywatch/agent-smith/internal/dnsbench"
	"github.com/NYBaywatch/agent-smith/internal/dnsprobe"
	"github.com/NYBaywatch/agent-smith/internal/ispinfo"
	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/netinfo"
	"github.com/NYBaywatch/agent-smith/internal/pathmon"
	"github.com/NYBaywatch/agent-smith/internal/speedtest"
	"github.com/NYBaywatch/agent-smith/internal/stability"
	"github.com/NYBaywatch/agent-smith/internal/synth"
	"github.com/NYBaywatch/agent-smith/internal/sysinfo"
)

// TargetStats is the rolling statistics for one probed target.
type TargetStats struct {
	Name  string // display name, e.g. "Gateway", "Cloudflare"
	Host  string // IP/host probed
	Role  Role   // ring this target represents
	Stats metrics.Stats
	Alive bool // received at least one reply in the current window
}

// Role identifies which ring of the path a target represents.
type Role int

const (
	RoleGateway  Role = iota // LAN / router
	RoleISPHop               // first public hop (ISP access edge)
	RoleInternet             // public anchor (1.1.1.1, 8.8.8.8, game server)
)

func (r Role) String() string {
	switch r {
	case RoleGateway:
		return "Gateway"
	case RoleISPHop:
		return "ISP hop"
	default:
		return "Internet"
	}
}

// Snapshot is a complete point-in-time view of connection health, produced by
// the engine on every tick and consumed by the classifier and the UIs.
type Snapshot struct {
	Time        time.Time
	Gateway     *TargetStats
	ISPHop      *TargetStats
	Internet    []TargetStats
	Net         *netinfo.Info
	Sys         sysinfo.Stats
	DNS         dnsprobe.Result
	DNSServers  []dnsprobe.ServerResult // per-resolver comparison
	Conn        *ispinfo.Info           // public IP / ISP / ASN (nil until looked up)
	Bufferbloat *bufferbloat.Result     // last on-demand test, nil until run
	Speed       *speedtest.Result       // last speed test, nil until run
	Stability   *stability.Result       // last probe-burst test, nil until run
	DNSBench    *dnsbench.Result        // last resolver benchmark, nil until run
	Verdict     Verdict

	// --- Internet performance monitoring (single-vantage-point IPM) ---

	// Synthetics holds the rolling result of every HTTP synthetic check
	// (SaaS / cloud / CDN / AI API / custom), in configured order.
	Synthetics []synth.Summary
	// Paths is the newest hop-by-hop traceroute per destination.
	Paths []pathmon.Path
	// RouteChanges lists recent detected path changes, newest first.
	RouteChanges []pathmon.RouteChange
	// PathDiag is the "where does degradation start" reading of the path to
	// the primary anchor (HopIndex -1 when the path is clean or unknown).
	PathDiag pathmon.Diagnosis
	// BGP is the RIPEstat reachability of the connection's own public prefix
	// (nil until looked up).
	BGP *bgp.Status
	// DNSAuth holds timed queries sent straight to authoritative nameservers.
	DNSAuth []dnsprobe.AuthResult
	// SLA carries the long-term availability / latency summaries, baseline and
	// SLO compliance for the key series.
	SLA []SLAEntry
	// Incident is the currently open incident (grouped from consecutive
	// degraded verdicts), nil when healthy.
	Incident *IncidentRef
	// AlertRaw / AlertIncidents are the "alert compression" counters: raw
	// degraded ticks observed vs incidents they were folded into.
	AlertRaw, AlertIncidents int
}

// SLAEntry is the long-term view of one monitored series.
type SLAEntry struct {
	Key        string // series key, e.g. "internet", "gateway", "http:<url>"
	Name       string // display name
	Kind       string // "ping" | "http"
	Hour       baseline.Summary
	Day        baseline.Summary
	Week       baseline.Summary
	Baseline   baseline.Baseline
	Compliance baseline.Compliance // against the configured SLO, over 24 h
	CurrentMs  float64             // latest value compared against the baseline
	Z          float64             // robust z-score of CurrentMs vs baseline
	Anomalous  bool
}

// IncidentRef is a lightweight copy of the open incident for snapshots.
type IncidentRef struct {
	ID       int
	Start    time.Time
	Culprit  Culprit
	Peak     Severity
	Ticks    int
	Headline string
}

// Culprit is the segment most likely responsible for degradation.
type Culprit int

const (
	CulpritHealthy Culprit = iota
	CulpritLocalMachine
	CulpritWiFi
	CulpritLANRouter
	CulpritISPAccess
	CulpritUpstream
	CulpritDNS
	CulpritRemoteService // a specific site/API/SaaS is failing while the path is fine
	CulpritUnknown
)

func (c Culprit) String() string {
	switch c {
	case CulpritHealthy:
		return "Healthy"
	case CulpritLocalMachine:
		return "Local machine"
	case CulpritWiFi:
		return "Wi-Fi"
	case CulpritLANRouter:
		return "LAN / router"
	case CulpritISPAccess:
		return "ISP access link"
	case CulpritUpstream:
		return "Upstream internet"
	case CulpritDNS:
		return "DNS"
	case CulpritRemoteService:
		return "Remote service"
	default:
		return "Unknown"
	}
}

// Severity ranks how bad the current state is, for UI coloring/alerts.
type Severity int

const (
	SevOK Severity = iota
	SevWatch
	SevDegraded
	SevCritical
)

func (s Severity) String() string {
	switch s {
	case SevOK:
		return "OK"
	case SevWatch:
		return "Watch"
	case SevDegraded:
		return "Degraded"
	case SevCritical:
		return "Critical"
	default:
		return "OK"
	}
}

// Verdict is the classifier's conclusion.
type Verdict struct {
	Culprit    Culprit
	Severity   Severity
	Confidence float64 // 0..1
	Headline   string  // one-line plain-language summary
	Detail     string  // supporting explanation
	Fix        string  // recommended remediation
}

// ProcInfo is a single process captured in an issue's snapshot (like `ps`).
type ProcInfo struct {
	PID   int32   `json:"pid"`
	Name  string  `json:"name"`
	CPU   float64 `json:"cpu"` // percent
	MemMB float64 `json:"mem_mb"`
}

// IssueMetrics captures the concrete measured values at the moment an issue was
// recorded — i.e. exactly what was degraded.
type IssueMetrics struct {
	GatewayMs        float64 `json:"gateway_ms"`
	ISPMs            float64 `json:"isp_ms"`
	InternetMs       float64 `json:"internet_ms"`
	InternetJitterMs float64 `json:"internet_jitter_ms"`
	InternetLossPct  float64 `json:"internet_loss_pct"`
	CPUPct           float64 `json:"cpu_pct"`
	MemPct           float64 `json:"mem_pct"`
	MemUsedGB        float64 `json:"mem_used_gb"`
	MemTotalGB       float64 `json:"mem_total_gb"`
	GPUPct           float64 `json:"gpu_pct"` // -1 if unavailable
	OnWiFi           bool    `json:"on_wifi"`
	RSSI             int     `json:"rssi"` // dBm, valid only when OnWiFi
	DNSms            float64 `json:"dns_ms"`
	Bufferbloat      string  `json:"bufferbloat"` // grade, "" if not measured
}

// Issue is a recorded degradation event: a timestamped verdict, the measured
// metrics that were degraded, plus a snapshot of the top processes running at
// that moment (to help correlate lag with apps).
type Issue struct {
	Time     time.Time    `json:"time"`
	Severity Severity     `json:"severity"`
	Culprit  Culprit      `json:"culprit"`
	Headline string       `json:"headline"`
	Detail   string       `json:"detail"`
	Fix      string       `json:"fix"`
	Metrics  IssueMetrics `json:"metrics"`
	Procs    []ProcInfo   `json:"procs"`
}

// HistPoint is one sampled moment of RTT history for the sparkline / persistence.
type HistPoint struct {
	T     time.Time `json:"t"`
	GwMs  float64   `json:"gw_ms"`  // gateway RTT in ms (0 = no data)
	IspMs float64   `json:"isp_ms"` // ISP-hop RTT in ms
	NetMs float64   `json:"net_ms"` // best internet anchor RTT in ms
}
