// Package web is the mobile-style Agent Smith user interface: a WebView2 page
// (Wails v2) driven by the engine's snapshot stream. This file holds the
// cross-platform, pure data-transfer layer — the JSON shapes the page consumes
// and the mapping from the engine's model into them — so it can be unit-tested
// anywhere. The Windows-only window, tray and notification plumbing lives in
// app_windows.go.
package web

import (
	"time"

	"github.com/NYBaywatch/agent-smith/internal/baseline"
	"github.com/NYBaywatch/agent-smith/internal/classifier"
	"github.com/NYBaywatch/agent-smith/internal/incident"
	"github.com/NYBaywatch/agent-smith/internal/interpret"
	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/model"
	"github.com/NYBaywatch/agent-smith/internal/pathmon"
	"github.com/NYBaywatch/agent-smith/internal/store"
	"github.com/NYBaywatch/agent-smith/internal/synth"
)

// --- DTO types (snake_case JSON per the design spec) ---

// Verdict is the classifier's conclusion.
type Verdict struct {
	Culprit      string  `json:"culprit"`
	Severity     int     `json:"severity"`
	SeverityName string  `json:"severity_name"`
	Confidence   float64 `json:"confidence"`
	Headline     string  `json:"headline"`
	Detail       string  `json:"detail"`
	Fix          string  `json:"fix"`
}

// Ring is one probed segment of the path.
type Ring struct {
	Ring     string  `json:"ring"` // LAN | ISP | NET
	Name     string  `json:"name"`
	Host     string  `json:"host"`
	Alive    bool    `json:"alive"`
	MeanMs   float64 `json:"mean_ms"`
	P95Ms    float64 `json:"p95_ms"`
	JitterMs float64 `json:"jitter_ms"`
	LossPct  float64 `json:"loss_pct"`
	Rating   string  `json:"rating"`
}

// Bufferbloat is the last on-demand latency-under-load result.
type Bufferbloat struct {
	Grade    string  `json:"grade"`
	AddedMs  float64 `json:"added_ms"`
	IdleMs   float64 `json:"idle_ms"`
	LoadedMs float64 `json:"loaded_ms"`
	DownMbps float64 `json:"down_mbps"`
}

// Headline carries the hero metrics from the healthiest internet anchor.
type Headline struct {
	LatencyMs     float64      `json:"latency_ms"`
	LatencyRating string       `json:"latency_rating"`
	JitterMs      float64      `json:"jitter_ms"`
	JitterRating  string       `json:"jitter_rating"`
	LossPct       float64      `json:"loss_pct"`
	LossRating    string       `json:"loss_rating"`
	Bufferbloat   *Bufferbloat `json:"bufferbloat"`
}

// Sys is host resource usage.
type Sys struct {
	CPUPct     float64 `json:"cpu_pct"`
	MemPct     float64 `json:"mem_pct"`
	MemUsedGB  float64 `json:"mem_used_gb"`
	MemTotalGB float64 `json:"mem_total_gb"`
	GPUPct     float64 `json:"gpu_pct"` // -1 when unavailable
	InMbps     float64 `json:"in_mbps"`
	OutMbps    float64 `json:"out_mbps"`
}

// WiFi is wireless link quality.
type WiFi struct {
	SSID    string  `json:"ssid"`
	RSSI    int     `json:"rssi"`
	Quality uint32  `json:"quality"`
	RxMbps  float64 `json:"rx_mbps"`
	TxMbps  float64 `json:"tx_mbps"`
}

// Net describes the active adapter.
type Net struct {
	Interface string `json:"interface"`
	Media     string `json:"media"`
	LinkMbps  uint64 `json:"link_mbps"`
	MTU       uint32 `json:"mtu"`
	Errors    uint64 `json:"errors"`
	Gateway   string `json:"gateway"`
	WiFi      *WiFi  `json:"wifi"`
}

// DNSServer is one resolver measurement.
type DNSServer struct {
	Name  string  `json:"name"`
	Addr  string  `json:"addr"`
	AvgMs float64 `json:"avg_ms"`
	OK    bool    `json:"ok"`
	Slow  bool    `json:"slow"`
}

// DNSAuth is one authoritative nameserver timing.
type DNSAuth struct {
	Domain string  `json:"domain"`
	NS     string  `json:"ns"`
	Addr   string  `json:"addr"`
	Ms     float64 `json:"ms"`
	OK     bool    `json:"ok"`
	Err    string  `json:"err"`
}

// DNS bundles resolution latency.
type DNS struct {
	AvgMs         float64     `json:"avg_ms"`
	Slow          bool        `json:"slow"`
	Lookups       int         `json:"lookups"`
	Failed        int         `json:"failed"`
	Servers       []DNSServer `json:"servers"`
	Authoritative []DNSAuth   `json:"authoritative"`
}

// Connection is the public identity of the link.
type Connection struct {
	IP         string `json:"ip"`
	ISP        string `json:"isp"`
	Org        string `json:"org"`
	ASN        string `json:"asn"`
	ASName     string `json:"as_name"`
	Location   string `json:"location"`
	Type       string `json:"type"`
	Reverse    string `json:"reverse"`
	Support    string `json:"support"`
	SupportURL string `json:"support_url"`
}

// BGP is RIPEstat visibility of the connection's own prefix.
type BGP struct {
	Prefix        string  `json:"prefix"`
	OriginASN     int     `json:"origin_asn"`
	VisibilityPct float64 `json:"visibility_pct"`
	VisiblePeers  int     `json:"visible_peers"`
	TotalPeers    int     `json:"total_peers"`
	Announced     bool    `json:"announced"`
	Healthy       bool    `json:"healthy"`
	FetchedAt     string  `json:"fetched_at"`
}

// ServiceDTO is one synthetic HTTP check.
type ServiceDTO struct {
	Name       string  `json:"name"`
	Category   string  `json:"category"`
	Provider   string  `json:"provider"`
	URL        string  `json:"url"`
	Status     string  `json:"status"` // ok | slow | fail | down | pending
	DNSMs      float64 `json:"dns_ms"`
	ConnectMs  float64 `json:"connect_ms"`
	TLSMs      float64 `json:"tls_ms"`
	TTFBMs     float64 `json:"ttfb_ms"`
	DownloadMs float64 `json:"download_ms"`
	TotalMs    float64 `json:"total_ms"`
	AvailPct   float64 `json:"avail_pct"`
	Runs       int     `json:"runs"`
	Edge       string  `json:"edge"`
	Error      string  `json:"error"`
	HTTPStatus int     `json:"http_status"`
	LastAt     string  `json:"last_at"`
}

// HopDTO is one traceroute hop.
type HopDTO struct {
	TTL      int     `json:"ttl"`
	Addr     string  `json:"addr"`
	Host     string  `json:"host"`
	ASN      int     `json:"asn"`
	ASName   string  `json:"as_name"`
	AvgMs    float64 `json:"avg_ms"`
	MaxMs    float64 `json:"max_ms"`
	LossPct  float64 `json:"loss_pct"`
	Private  bool    `json:"private"`
	Segment  string  `json:"segment"`
	Degraded bool    `json:"degraded"`
}

// Diagnosis names where degradation starts.
type Diagnosis struct {
	HopIndex int    `json:"hop_index"`
	Reason   string `json:"reason"`
	Segment  string `json:"segment"`
}

// PathDTO is one traceroute.
type PathDTO struct {
	Name      string    `json:"name"`
	Dest      string    `json:"dest"`
	When      string    `json:"when"`
	Reached   bool      `json:"reached"`
	ASPath    string    `json:"as_path"`
	ElapsedMs float64   `json:"elapsed_ms"`
	Hops      []HopDTO  `json:"hops"`
	Diagnosis Diagnosis `json:"diagnosis"`
	Error     string    `json:"error,omitempty"`
}

// RouteChange is a detected path change.
type RouteChange struct {
	When       string `json:"when"`
	Name       string `json:"name"`
	Dest       string `json:"dest"`
	HopsBefore int    `json:"hops_before"`
	HopsAfter  int    `json:"hops_after"`
	ASBefore   string `json:"as_before"`
	ASAfter    string `json:"as_after"`
}

// SLA is the long-term view of one series.
type SLA struct {
	Key            string  `json:"key"`
	Name           string  `json:"name"`
	Kind           string  `json:"kind"`
	Avail1hPct     float64 `json:"avail_1h_pct"`
	Avail24hPct    float64 `json:"avail_24h_pct"`
	Avail7dPct     float64 `json:"avail_7d_pct"`
	P9524hMs       float64 `json:"p95_24h_ms"`
	Mean24hMs      float64 `json:"mean_24h_ms"`
	Samples24h     int     `json:"samples_24h"`
	BaselineMs     float64 `json:"baseline_ms"`
	BaselineValid  bool    `json:"baseline_valid"`
	BudgetLeftPct  float64 `json:"budget_left_pct"`
	SLOOK          bool    `json:"slo_ok"`
	AvailabilityOK bool    `json:"availability_ok"`
	LatencyOK      bool    `json:"latency_ok"`
	NowMs          float64 `json:"now_ms"`
	Z              float64 `json:"z"`
	Anomalous      bool    `json:"anomalous"`
}

// SLO is the objective SLA compliance is judged against.
type SLO struct {
	AvailabilityPct float64 `json:"availability_pct"`
	P95Ms           float64 `json:"p95_ms"`
}

// IncidentRef is the open incident, if any.
type IncidentRef struct {
	ID        int     `json:"id"`
	Start     string  `json:"start"`
	DurationS float64 `json:"duration_s"`
	Culprit   string  `json:"culprit"`
	Peak      int     `json:"peak"`
	PeakName  string  `json:"peak_name"`
	Ticks     int     `json:"ticks"`
	Headline  string  `json:"headline"`
}

// Alerts are the alert-compression counters.
type Alerts struct {
	Raw       int `json:"raw"`
	Incidents int `json:"incidents"`
}

// Snapshot is the complete view the page renders.
type Snapshot struct {
	Time         string        `json:"time"`
	Verdict      Verdict       `json:"verdict"`
	Rings        []Ring        `json:"rings"`
	Headline     Headline      `json:"headline"`
	Sys          Sys           `json:"sys"`
	Net          *Net          `json:"net"`
	DNS          DNS           `json:"dns"`
	Connection   *Connection   `json:"connection"`
	BGP          *BGP          `json:"bgp"`
	Services     []ServiceDTO  `json:"services"`
	Paths        []PathDTO     `json:"paths"`
	RouteChanges []RouteChange `json:"route_changes"`
	SLA          []SLA         `json:"sla"`
	SLO          SLO           `json:"slo"`
	Incident     *IncidentRef  `json:"incident"`
	Alerts       Alerts        `json:"alerts"`
	Tests        TestsDTO      `json:"tests"`
}

// HistPoint is one downsampled RTT history point.
type HistPoint struct {
	T   string  `json:"t"`
	Gw  float64 `json:"gw"`
	Isp float64 `json:"isp"`
	Net float64 `json:"net"`
}

// IssueLine is one interpreted measurement.
type IssueLine struct {
	Label   string `json:"label"`
	Value   string `json:"value"`
	Rating  string `json:"rating"`
	Meaning string `json:"meaning"`
}

// ProcDTO is one process in an issue snapshot.
type ProcDTO struct {
	PID   int32   `json:"pid"`
	Name  string  `json:"name"`
	CPU   float64 `json:"cpu"`
	MemMB float64 `json:"mem_mb"`
}

// IssueDTO is one recorded event.
type IssueDTO struct {
	Time         string      `json:"time"`
	Severity     int         `json:"severity"`
	SeverityName string      `json:"severity_name"`
	Culprit      string      `json:"culprit"`
	Headline     string      `json:"headline"`
	Detail       string      `json:"detail"`
	Fix          string      `json:"fix"`
	Summary      string      `json:"summary"`
	Lines        []IssueLine `json:"lines"`
	Procs        []ProcDTO   `json:"procs"`
}

// IncidentDTO is one grouped incident.
type IncidentDTO struct {
	ID        int      `json:"id"`
	Start     string   `json:"start"`
	End       string   `json:"end"`
	Open      bool     `json:"open"`
	DurationS float64  `json:"duration_s"`
	Culprit   string   `json:"culprit"`
	Peak      int      `json:"peak"`
	PeakName  string   `json:"peak_name"`
	Ticks     int      `json:"ticks"`
	Headline  string   `json:"headline"`
	Last      string   `json:"last"`
	Fix       string   `json:"fix"`
	Culprits  []string `json:"culprits"`
}

// Info describes the running app.
type Info struct {
	Version    string `json:"version"`
	ConfigPath string `json:"config_path"`
	StatePath  string `json:"state_path"`
	Started    string `json:"started"`
}

// BufferbloatResult is the outcome of an on-demand test.
type BufferbloatResult struct {
	Grade    string  `json:"grade"`
	AddedMs  float64 `json:"added_ms"`
	IdleMs   float64 `json:"idle_ms"`
	LoadedMs float64 `json:"loaded_ms"`
	DownMbps float64 `json:"down_mbps"`
	Source   string  `json:"source,omitempty"`
	Colo     string  `json:"colo,omitempty"`
	Error    string  `json:"error,omitempty"`
}

// --- mapping ---

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func rfc(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// ratingWord maps a metrics.Rating to the UI vocabulary.
func ratingWord(r metrics.Rating) string {
	switch r {
	case metrics.RatingExcellent:
		return "Excellent"
	case metrics.RatingGood:
		return "Good"
	case metrics.RatingPlayable:
		return "Fair"
	case metrics.RatingPoor:
		return "Poor"
	default:
		return "—"
	}
}

func ringOf(label string, ts *model.TargetStats) Ring {
	r := Ring{Ring: label, Name: ts.Name, Host: ts.Host, Alive: ts.Alive, Rating: "—"}
	if ts.Alive {
		st := ts.Stats
		r.MeanMs = ms(st.Mean)
		r.P95Ms = ms(st.P95)
		r.JitterMs = ms(st.Jitter)
		r.LossPct = st.Loss * 100
		r.Rating = ratingWord(metrics.RateLatency(st.Mean))
	}
	return r
}

// bestInternet returns the healthiest alive anchor (lowest mean RTT), or nil.
func bestInternet(s model.Snapshot) *model.TargetStats {
	var best *model.TargetStats
	for i := range s.Internet {
		ts := &s.Internet[i]
		if !ts.Alive {
			continue
		}
		if best == nil || ts.Stats.Mean < best.Stats.Mean {
			best = ts
		}
	}
	return best
}

// BuildSnapshot maps the engine's snapshot into the page's DTO. It is
// nil-safe: a zero model.Snapshot yields empty slices (never null) and nil
// optional sections.
func BuildSnapshot(s model.Snapshot, slo baseline.SLO) Snapshot {
	out := Snapshot{
		Time: rfc(s.Time),
		Verdict: Verdict{
			Culprit:      s.Verdict.Culprit.String(),
			Severity:     int(s.Verdict.Severity),
			SeverityName: s.Verdict.Severity.String(),
			Confidence:   s.Verdict.Confidence,
			Headline:     s.Verdict.Headline,
			Detail:       s.Verdict.Detail,
			Fix:          s.Verdict.Fix,
		},
		Rings:        []Ring{},
		Services:     []ServiceDTO{},
		Paths:        []PathDTO{},
		RouteChanges: []RouteChange{},
		SLA:          []SLA{},
		SLO:          SLO{AvailabilityPct: slo.Availability * 100, P95Ms: slo.P95Ms},
		Alerts:       Alerts{Raw: s.AlertRaw, Incidents: s.AlertIncidents},
		Headline:     Headline{LatencyRating: "—", JitterRating: "—", LossRating: "—"},
		DNS:          DNS{Servers: []DNSServer{}, Authoritative: []DNSAuth{}},
	}

	// Rings.
	if s.Gateway != nil {
		out.Rings = append(out.Rings, ringOf("LAN", s.Gateway))
	}
	if s.ISPHop != nil {
		out.Rings = append(out.Rings, ringOf("ISP", s.ISPHop))
	}
	for i := range s.Internet {
		out.Rings = append(out.Rings, ringOf("NET", &s.Internet[i]))
	}

	// Headline metrics.
	if b := bestInternet(s); b != nil {
		st := b.Stats
		out.Headline.LatencyMs = ms(st.Mean)
		out.Headline.LatencyRating = ratingWord(metrics.RateLatency(st.Mean))
		out.Headline.JitterMs = ms(st.Jitter)
		out.Headline.JitterRating = ratingWord(metrics.RateJitter(st.Jitter))
		out.Headline.LossPct = st.Loss * 100
		out.Headline.LossRating = ratingWord(metrics.RateLoss(st.Loss))
	}
	out.Tests = BuildTests(store.Tests{Bufferbloat: s.Bufferbloat, Speed: s.Speed, Stability: s.Stability, DNSBench: s.DNSBench})
	if bb := s.Bufferbloat; bb != nil {
		out.Headline.Bufferbloat = &Bufferbloat{
			Grade: bb.Grade, AddedMs: ms(bb.Added), IdleMs: ms(bb.IdleRTT), LoadedMs: ms(bb.LoadedRTT), DownMbps: bb.DownloadMbps,
		}
	}

	// System.
	out.Sys = Sys{
		CPUPct: s.Sys.CPUPercent, MemPct: s.Sys.MemPercent, MemUsedGB: s.Sys.MemUsedGB, MemTotalGB: s.Sys.MemTotalGB,
		GPUPct: s.Sys.GPUPercent, InMbps: s.Sys.InMbps, OutMbps: s.Sys.OutMbps,
	}

	// Network.
	if s.Net != nil {
		n := &Net{}
		if s.Net.GatewayIP != nil {
			n.Gateway = s.Net.GatewayIP.String()
		}
		if a := s.Net.Active; a != nil {
			n.Interface = a.Name
			n.Media = a.Media.String()
			n.LinkMbps = a.LinkMbps
			n.MTU = a.MTU
			n.Errors = a.InErrors + a.OutErrors + a.InDiscards + a.OutDiscards
		}
		if w := s.Net.WiFi; w != nil {
			n.WiFi = &WiFi{SSID: w.SSID, RSSI: w.RSSI, Quality: w.SignalQuality, RxMbps: w.RxMbps, TxMbps: w.TxMbps}
		}
		out.Net = n
	}

	// DNS.
	out.DNS.AvgMs = ms(s.DNS.Avg)
	out.DNS.Slow = s.DNS.Slow()
	out.DNS.Lookups = s.DNS.Lookups
	out.DNS.Failed = s.DNS.Failed
	for _, d := range s.DNSServers {
		out.DNS.Servers = append(out.DNS.Servers, DNSServer{Name: d.Name, Addr: d.Addr, AvgMs: ms(d.Avg), OK: d.OK(), Slow: d.Slow()})
	}
	for _, a := range s.DNSAuth {
		out.DNS.Authoritative = append(out.DNS.Authoritative, DNSAuth{Domain: a.Domain, NS: a.NS, Addr: a.Addr, Ms: ms(a.Latency), OK: a.OK, Err: a.Err})
	}

	// Connection / BGP.
	if c := s.Conn; c != nil {
		out.Connection = &Connection{
			IP: c.IP, ISP: c.ISP, Org: c.Org, ASN: c.ASN(), ASName: c.ASName, Location: c.Location(),
			Type: c.ConnType, Reverse: c.Reverse, Support: c.Support, SupportURL: c.SupportURL,
		}
	}
	if b := s.BGP; b != nil {
		out.BGP = &BGP{
			Prefix: b.Prefix, OriginASN: b.OriginASN, VisibilityPct: b.Visibility * 100, VisiblePeers: b.VisiblePeers,
			TotalPeers: b.TotalPeers, Announced: b.Announced, Healthy: b.Healthy(), FetchedAt: rfc(b.FetchedAt),
		}
	}

	// Services.
	out.Services = BuildServices(s.Synthetics)

	// Paths.
	for i, p := range s.Paths {
		var d pathmon.Diagnosis
		if i == 0 {
			d = s.PathDiag
		} else {
			d = pathmon.FirstDegradedHop(p, classifier.PathLossThreshold, classifier.PathJumpThreshold)
		}
		out.Paths = append(out.Paths, BuildPath(p, d))
	}
	for _, c := range s.RouteChanges {
		out.RouteChanges = append(out.RouteChanges, RouteChange{
			When: rfc(c.When), Name: c.Name, Dest: c.Dest, HopsBefore: c.HopsBefore, HopsAfter: c.HopsAfter, ASBefore: c.ASBefore, ASAfter: c.ASAfter,
		})
	}

	// SLA.
	for _, e := range s.SLA {
		out.SLA = append(out.SLA, SLA{
			Key: e.Key, Name: e.Name, Kind: e.Kind,
			Avail1hPct: e.Hour.Availability * 100, Avail24hPct: e.Day.Availability * 100, Avail7dPct: e.Week.Availability * 100,
			P9524hMs: e.Day.P95, Mean24hMs: e.Day.Mean, Samples24h: e.Day.Samples,
			BaselineMs: e.Baseline.Median, BaselineValid: e.Baseline.Valid,
			BudgetLeftPct: e.Compliance.ErrorBudgetLeft * 100, SLOOK: e.Compliance.OK,
			AvailabilityOK: e.Compliance.AvailabilityOK, LatencyOK: e.Compliance.LatencyOK,
			NowMs: e.CurrentMs, Z: e.Z, Anomalous: e.Anomalous,
		})
	}

	// Incident.
	if in := s.Incident; in != nil {
		out.Incident = &IncidentRef{
			ID: in.ID, Start: rfc(in.Start), DurationS: s.Time.Sub(in.Start).Seconds(),
			Culprit: in.Culprit.String(), Peak: int(in.Peak), PeakName: in.Peak.String(), Ticks: in.Ticks, Headline: in.Headline,
		}
	}
	return out
}

// BuildServices maps synthetic summaries to service DTOs.
func BuildServices(sums []synth.Summary) []ServiceDTO {
	out := make([]ServiceDTO, 0, len(sums))
	for _, sm := range sums {
		st := sm.Stats
		d := ServiceDTO{
			Name: sm.Check.Name, Category: string(sm.Check.Category), Provider: sm.Check.Provider, URL: sm.Check.URL,
			Status: "pending", Runs: st.Runs,
		}
		if st.Runs > 0 {
			switch {
			case st.Down():
				d.Status = "down"
			case !st.Last.OK:
				d.Status = "fail"
			case st.Slow():
				d.Status = "slow"
			default:
				d.Status = "ok"
			}
			d.DNSMs = ms(st.MeanDNS)
			d.ConnectMs = ms(st.MeanConnect)
			d.TLSMs = ms(st.MeanTLS)
			d.TTFBMs = ms(st.MeanTTFB)
			d.DownloadMs = ms(st.Last.Timing.Download)
			d.TotalMs = ms(st.Mean)
			d.AvailPct = st.Availability * 100
			d.Edge = st.Last.Edge
			d.Error = st.Last.Err
			d.HTTPStatus = st.Last.Status
			d.LastAt = rfc(st.Last.When)
		}
		out = append(out, d)
	}
	return out
}

// BuildPath maps a traceroute plus its diagnosis to the DTO.
func BuildPath(p pathmon.Path, d pathmon.Diagnosis) PathDTO {
	if len(p.ASPath) == 0 {
		// Paths built outside Trace (tests, on-demand) may lack the AS path.
		p.ASPath = pathmon.ComputeASPath(p.Hops)
	}
	out := PathDTO{
		Name: p.Name, Dest: p.Dest, When: rfc(p.When), Reached: p.Reached, ASPath: p.ASSignature(), ElapsedMs: ms(p.Elapsed),
		Hops:      make([]HopDTO, 0, len(p.Hops)),
		Diagnosis: Diagnosis{HopIndex: d.HopIndex, Reason: d.Reason, Segment: d.Segment.String()},
	}
	if len(p.Hops) == 0 {
		out.Diagnosis.HopIndex = -1
	}
	segs := pathmon.Segments(p)
	for i, h := range p.Hops {
		seg := ""
		if i < len(segs) {
			seg = segs[i].String()
		}
		out.Hops = append(out.Hops, HopDTO{
			TTL: h.TTL, Addr: h.Addr, Host: h.Host, ASN: h.ASN, ASName: h.ASName,
			AvgMs: ms(h.Avg), MaxMs: ms(h.Max), LossPct: h.Loss * 100, Private: h.Private,
			Segment: seg, Degraded: i == d.HopIndex,
		})
	}
	return out
}

// BuildHistory downsamples RTT history to at most max points by averaging
// equal-size buckets (zeros are ignored in the average; a bucket with only
// zeros stays 0). Output is oldest first.
func BuildHistory(h []model.HistPoint, max int) []HistPoint {
	out := make([]HistPoint, 0, max)
	if len(h) == 0 {
		return out
	}
	if max <= 0 || len(h) <= max {
		for _, p := range h {
			out = append(out, HistPoint{T: rfc(p.T), Gw: p.GwMs, Isp: p.IspMs, Net: p.NetMs})
		}
		return out
	}
	avg := func(vals []float64) float64 {
		var sum float64
		var n int
		for _, v := range vals {
			if v > 0 {
				sum += v
				n++
			}
		}
		if n == 0 {
			return 0
		}
		return sum / float64(n)
	}
	for b := 0; b < max; b++ {
		lo := b * len(h) / max
		hi := (b + 1) * len(h) / max
		if hi <= lo {
			hi = lo + 1
		}
		if hi > len(h) {
			hi = len(h)
		}
		chunk := h[lo:hi]
		gw := make([]float64, 0, len(chunk))
		isp := make([]float64, 0, len(chunk))
		net := make([]float64, 0, len(chunk))
		for _, p := range chunk {
			gw = append(gw, p.GwMs)
			isp = append(isp, p.IspMs)
			net = append(net, p.NetMs)
		}
		out = append(out, HistPoint{T: rfc(chunk[len(chunk)-1].T), Gw: avg(gw), Isp: avg(isp), Net: avg(net)})
	}
	return out
}

// BuildIssues maps recorded events (oldest first in the engine) to DTOs,
// newest first, with the plain-language interpretation attached.
func BuildIssues(list []model.Issue) []IssueDTO {
	out := make([]IssueDTO, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		is := list[i]
		d := IssueDTO{
			Time: rfc(is.Time), Severity: int(is.Severity), SeverityName: is.Severity.String(), Culprit: is.Culprit.String(),
			Headline: is.Headline, Detail: is.Detail, Fix: is.Fix, Summary: interpret.Summary(is),
			Lines: []IssueLine{}, Procs: []ProcDTO{},
		}
		for _, l := range interpret.Lines(is.Metrics) {
			d.Lines = append(d.Lines, IssueLine{Label: l.Label, Value: l.Value, Rating: l.Rating, Meaning: l.Meaning})
		}
		for _, p := range is.Procs {
			d.Procs = append(d.Procs, ProcDTO{PID: p.PID, Name: p.Name, CPU: p.CPU, MemMB: p.MemMB})
		}
		out = append(out, d)
	}
	return out
}

// BuildIncidents maps grouped incidents (already newest first) to DTOs.
func BuildIncidents(list []incident.Incident, now time.Time) []IncidentDTO {
	out := make([]IncidentDTO, 0, len(list))
	for _, in := range list {
		out = append(out, BuildIncident(in, now))
	}
	return out
}

// BuildIncident maps one incident.
func BuildIncident(in incident.Incident, now time.Time) IncidentDTO {
	d := IncidentDTO{
		ID: in.ID, Start: rfc(in.Start), End: rfc(in.End), Open: in.Open(), DurationS: in.Duration(now).Seconds(),
		Culprit: in.Culprit.String(), Peak: int(in.Peak), PeakName: in.Peak.String(), Ticks: in.Ticks,
		Headline: in.Headline, Last: in.Last, Fix: in.Fix, Culprits: []string{},
	}
	if in.Open() {
		d.End = ""
	}
	d.Culprits = append(d.Culprits, in.Culprits...)
	return d
}
