// Package model holds the shared data types exchanged between the engine, the
// bottleneck classifier, and the user interfaces. Keeping them here avoids an
// import cycle (engine → classifier → model ← ui).
package model

import (
	"time"

	"github.com/NYBaywatch/agent-smith/internal/bufferbloat"
	"github.com/NYBaywatch/agent-smith/internal/dnsprobe"
	"github.com/NYBaywatch/agent-smith/internal/ispinfo"
	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/netinfo"
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
	Verdict     Verdict
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
