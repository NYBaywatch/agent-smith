// Package classifier turns a Snapshot into a Verdict: which segment of the path
// is most likely responsible for degraded gaming experience. It evaluates rules
// most-local-first (a local fault masks everything downstream) following the
// decision heuristic in docs/DESIGN.md.
package classifier

import (
	"fmt"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/model"
	"github.com/NYBaywatch/agent-smith/internal/netinfo"
)

// Thresholds collects the tunable limits used by the classifier. Defaults come
// from the research brief (docs/RESEARCH.md / docs/DESIGN.md).
type Thresholds struct {
	CPUHigh           float64       // % CPU considered saturating
	MemHigh           float64       // % memory considered pressured
	GatewayRTTWired   time.Duration // healthy LAN RTT ceiling (wired)
	GatewayRTTWiFi    time.Duration // healthy LAN RTT ceiling (Wi-Fi)
	GatewayJitter     time.Duration // healthy LAN jitter ceiling
	WiFiRSSIPoor      int           // dBm at/below which Wi-Fi is poor
	WiFiRSSIMarginal  int           // dBm at/below which Wi-Fi is marginal
	InternetRTTPoor   time.Duration // internet RTT considered poor
	JitterPoor        time.Duration
	LossPoor          float64
	LossWatch         float64
	DNSSlow           time.Duration
	NICErrThreshold   uint64 // error+discard count that flags a NIC problem
	SelfThroughputHi  float64
	BufferbloatBadMin string // grades >= this letter are "bad" (C/D/F)
}

// DefaultThresholds returns the standard tunables.
func DefaultThresholds() Thresholds {
	return Thresholds{
		CPUHigh:          90,
		MemHigh:          92,
		GatewayRTTWired:  5 * time.Millisecond,
		GatewayRTTWiFi:   15 * time.Millisecond,
		GatewayJitter:    8 * time.Millisecond,
		WiFiRSSIPoor:     -75,
		WiFiRSSIMarginal: -67,
		InternetRTTPoor:  120 * time.Millisecond,
		JitterPoor:       30 * time.Millisecond,
		LossPoor:         0.025,
		LossWatch:        0.01,
		DNSSlow:          150 * time.Millisecond,
		NICErrThreshold:  100,
		SelfThroughputHi: 50, // Mbps of self-generated traffic
	}
}

// Classify evaluates a snapshot and returns a Verdict. It is deterministic and
// has no side effects, which makes it straightforward to unit-test.
func Classify(s model.Snapshot) model.Verdict {
	return ClassifyWith(s, DefaultThresholds())
}

// ClassifyWith is Classify with explicit thresholds (used by tests).
func ClassifyWith(s model.Snapshot, t Thresholds) model.Verdict {
	onWiFi := s.Net.OnWiFi()

	// --- Rule 1: Local machine (CPU/memory/NIC saturation) ---
	if s.Sys.CPUPercent >= t.CPUHigh {
		return model.Verdict{
			Culprit:    model.CulpritLocalMachine,
			Severity:   model.SevDegraded,
			Confidence: 0.8,
			Headline:   fmt.Sprintf("Your PC is maxed out (CPU %.0f%%)", s.Sys.CPUPercent),
			Detail:     "Sustained high CPU can delay packet processing and input handling, which feels exactly like network lag even when the link is fine.",
			Fix:        "Close background apps (browsers, launchers, recording/streaming software) and recheck. Consider Windows Game Mode.",
		}
	}
	if nicErr := nicErrors(s.Net); nicErr >= t.NICErrThreshold {
		return model.Verdict{
			Culprit:    model.CulpritLocalMachine,
			Severity:   model.SevDegraded,
			Confidence: 0.7,
			Headline:   "Network adapter is reporting errors",
			Detail:     fmt.Sprintf("The active NIC has %d cumulative errors/discards, which points to a driver, cable, or port fault.", nicErr),
			Fix:        "Update/reinstall the network driver, try a different cable/port, and disable aggressive NIC power-saving.",
		}
	}

	// --- Rule 2: Wi-Fi (only when actually on Wi-Fi) ---
	if onWiFi && s.Net.WiFi != nil {
		rssi := s.Net.WiFi.RSSI
		gwBad := s.Gateway != nil && (s.Gateway.Stats.Mean > t.GatewayRTTWiFi || s.Gateway.Stats.Jitter > t.GatewayJitter || s.Gateway.Stats.Loss > t.LossWatch)
		if rssi <= t.WiFiRSSIPoor || (rssi <= t.WiFiRSSIMarginal && gwBad) {
			sev := model.SevDegraded
			if rssi <= t.WiFiRSSIPoor {
				sev = model.SevCritical
			}
			return model.Verdict{
				Culprit:    model.CulpritWiFi,
				Severity:   sev,
				Confidence: 0.8,
				Headline:   fmt.Sprintf("Weak/unstable Wi-Fi (RSSI %d dBm)", rssi),
				Detail:     "The wireless link between you and the router is the weak point — low signal causes retransmits, jitter, and loss before traffic even reaches the internet.",
				Fix:        "Move closer to the router, switch to 5/6 GHz, reduce interference, or (best) use a wired Ethernet connection for gaming.",
			}
		}
	}

	// --- Rule 3: LAN / router (gateway bad while wired) ---
	if s.Gateway != nil && s.Gateway.Alive {
		ceiling := t.GatewayRTTWired
		if onWiFi {
			ceiling = t.GatewayRTTWiFi
		}
		if s.Gateway.Stats.Loss > t.LossWatch || s.Gateway.Stats.Mean > ceiling || s.Gateway.Stats.Jitter > t.GatewayJitter {
			// On Wi-Fi this is more likely the air link (handled above); here we
			// only reach this when wired or Wi-Fi signal looked fine.
			culprit := model.CulpritLANRouter
			head := "Local network / router is adding latency"
			fix := "Reboot the router, check Ethernet cabling/switch, and verify nothing on the LAN is saturating it."
			if onWiFi {
				culprit = model.CulpritWiFi
				head = "Local Wi-Fi link is adding latency"
				fix = "Try a wired connection or improve Wi-Fi placement/channel."
			}
			return model.Verdict{
				Culprit:    culprit,
				Severity:   model.SevDegraded,
				Confidence: 0.7,
				Headline:   head,
				Detail:     fmt.Sprintf("Round-trip to your own gateway is %s (jitter %s, loss %.1f%%) — the problem starts inside your home, before the ISP.", round(s.Gateway.Stats.Mean), round(s.Gateway.Stats.Jitter), s.Gateway.Stats.Loss*100),
				Fix:        fix,
			}
		}
	}

	// --- Rule 4: ISP access link (gateway fine, bufferbloat or ISP-hop jump) ---
	if s.Bufferbloat != nil && gradeIsBad(s.Bufferbloat.Grade) {
		return model.Verdict{
			Culprit:    model.CulpritISPAccess,
			Severity:   model.SevDegraded,
			Confidence: 0.85,
			Headline:   fmt.Sprintf("Bufferbloat on your access link (grade %s, +%s under load)", s.Bufferbloat.Grade, round(s.Bufferbloat.Added)),
			Detail:     "Your LAN is fine, but latency balloons when the connection is busy — classic bufferbloat in the modem/router queue on the ISP access link.",
			Fix:        "Enable Smart Queue Management (SQM / fq_codel / CAKE) on your router, or set QoS/bandwidth limits. More bandwidth will NOT fix this.",
		}
	}
	if s.Gateway != nil && s.Gateway.Alive && s.ISPHop != nil && s.ISPHop.Alive {
		// Gateway healthy but a large jump appears at the ISP edge and persists.
		jump := s.ISPHop.Stats.Mean - s.Gateway.Stats.Mean
		if (s.ISPHop.Stats.Loss > t.LossPoor || s.ISPHop.Stats.Jitter > t.JitterPoor || jump > t.InternetRTTPoor) && internetDegraded(s, t) {
			return model.Verdict{
				Culprit:    model.CulpritISPAccess,
				Severity:   model.SevDegraded,
				Confidence: 0.65,
				Headline:   "ISP access link looks degraded",
				Detail:     fmt.Sprintf("Your gateway is healthy, but the first ISP hop adds significant latency/loss (jitter %s, loss %.1f%%) that carries through to the internet.", round(s.ISPHop.Stats.Jitter), s.ISPHop.Stats.Loss*100),
				Fix:        "This is outside your home — contact your ISP with these figures, especially if it recurs at the same times of day (congestion).",
			}
		}
	}

	// --- Rule 5: Upstream internet (LAN+ISP fine, public anchors bad) ---
	if internetDegraded(s, t) && (s.Gateway == nil || !gatewayDegraded(s, t, onWiFi)) {
		best := bestInternet(s)
		return model.Verdict{
			Culprit:    model.CulpritUpstream,
			Severity:   model.SevDegraded,
			Confidence: 0.6,
			Headline:   "Problem is upstream / on the route to servers",
			Detail:     fmt.Sprintf("Your local network and ISP edge look fine, but reaching public hosts is poor (best anchor: %s, jitter %s, loss %.1f%%). The issue is in the wider internet or the game server's path.", round(best.Stats.Mean), round(best.Stats.Jitter), best.Stats.Loss*100),
			Fix:        "Usually transient peering/route congestion you can't control. Try a different server region, or a reputable VPN if a specific route is bad.",
		}
	}

	// --- Rule 6: DNS (resolution slow while RTTs are fine) ---
	if s.DNS.Avg > t.DNSSlow && !internetDegraded(s, t) {
		return model.Verdict{
			Culprit:    model.CulpritDNS,
			Severity:   model.SevWatch,
			Confidence: 0.7,
			Headline:   fmt.Sprintf("Slow DNS resolution (%s avg)", round(s.DNS.Avg)),
			Detail:     "Connectivity is fine, but turning names into addresses is slow, which makes launching games/matchmaking feel sluggish.",
			Fix:        "Switch your DNS to a fast resolver such as 1.1.1.1 (Cloudflare) or 8.8.8.8 (Google).",
		}
	}

	// --- Healthy ---
	sev := model.SevOK
	if internetWatch(s, t) {
		sev = model.SevWatch
	}
	return model.Verdict{
		Culprit:    model.CulpritHealthy,
		Severity:   sev,
		Confidence: 0.9,
		Headline:   "Connection looks good for gaming",
		Detail:     internetSummary(s),
		Fix:        "",
	}
}

func nicErrors(n *netinfo.Info) uint64 {
	if n == nil || n.Active == nil {
		return 0
	}
	a := n.Active
	return a.InErrors + a.OutErrors + a.InDiscards + a.OutDiscards
}

func gradeIsBad(grade string) bool {
	switch grade {
	case "C", "D", "F":
		return true
	default:
		return false
	}
}

// internetDegraded reports whether reaching public anchors is poor.
func internetDegraded(s model.Snapshot, t Thresholds) bool {
	if len(s.Internet) == 0 {
		return false
	}
	best := bestInternet(s)
	if !best.Alive {
		return true
	}
	return best.Stats.Loss > t.LossPoor ||
		best.Stats.Jitter > t.JitterPoor ||
		best.Stats.Mean > t.InternetRTTPoor
}

func internetWatch(s model.Snapshot, t Thresholds) bool {
	if len(s.Internet) == 0 {
		return false
	}
	best := bestInternet(s)
	return best.Alive && (best.Stats.Loss > t.LossWatch || best.Stats.Jitter > t.JitterPoor/2)
}

func gatewayDegraded(s model.Snapshot, t Thresholds, onWiFi bool) bool {
	if s.Gateway == nil || !s.Gateway.Alive {
		return false
	}
	ceiling := t.GatewayRTTWired
	if onWiFi {
		ceiling = t.GatewayRTTWiFi
	}
	return s.Gateway.Stats.Loss > t.LossWatch || s.Gateway.Stats.Mean > ceiling || s.Gateway.Stats.Jitter > t.GatewayJitter
}

// bestInternet returns the healthiest anchor (lowest mean among alive ones, or
// the first if none are alive) — we blame the internet only if even the best is bad.
func bestInternet(s model.Snapshot) model.TargetStats {
	var best model.TargetStats
	found := false
	for _, ts := range s.Internet {
		if !ts.Alive {
			continue
		}
		if !found || ts.Stats.Mean < best.Stats.Mean {
			best = ts
			found = true
		}
	}
	if !found && len(s.Internet) > 0 {
		return s.Internet[0]
	}
	return best
}

func internetSummary(s model.Snapshot) string {
	if len(s.Internet) == 0 {
		return "Awaiting samples…"
	}
	b := bestInternet(s)
	return fmt.Sprintf("Internet RTT ~%s, jitter %s, loss %.1f%% (rated %s).",
		round(b.Stats.Mean), round(b.Stats.Jitter), b.Stats.Loss*100, metrics.RateLatency(b.Stats.Mean))
}

func round(d time.Duration) time.Duration {
	if d >= time.Millisecond {
		return d.Round(100 * time.Microsecond)
	}
	return d.Round(10 * time.Microsecond)
}
