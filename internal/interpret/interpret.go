// Package interpret turns raw measurements into plain-language meaning: for each
// metric, a rating word and a sentence on its real-world impact, plus an overall
// "what this means for you" summary for a recorded issue. It is pure and
// cross-platform so both the GUI and CLI can use it and it can be unit-tested.
package interpret

import (
	"fmt"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/model"
)

// Line is one interpreted measurement.
type Line struct {
	Label   string // e.g. "Latency"
	Value   string // e.g. "95 ms"
	Rating  string // EXCELLENT | GOOD | FAIR | POOR | INFO
	Meaning string // what it means in practice
}

func ratingWord(r metrics.Rating) string {
	switch r {
	case metrics.RatingExcellent:
		return "EXCELLENT"
	case metrics.RatingGood:
		return "GOOD"
	case metrics.RatingPlayable:
		return "FAIR"
	case metrics.RatingPoor:
		return "POOR"
	default:
		return "INFO"
	}
}

func ms(v float64) string {
	if v <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f ms", v)
}

// Lines returns interpreted lines for the measurements behind an issue, in a
// sensible reading order.
func Lines(m model.IssueMetrics) []Line {
	var out []Line

	// Internet latency / jitter / loss — the headline real-time metrics.
	out = append(out, Line{
		Label: "Latency", Value: ms(m.InternetMs),
		Rating:  ratingWord(metrics.RateLatency(d(m.InternetMs))),
		Meaning: latencyMeaning(m.InternetMs),
	})
	out = append(out, Line{
		Label: "Jitter", Value: ms(m.InternetJitterMs),
		Rating:  ratingWord(metrics.RateJitter(d(m.InternetJitterMs))),
		Meaning: jitterMeaning(m.InternetJitterMs),
	})
	out = append(out, Line{
		Label: "Packet loss", Value: fmt.Sprintf("%.1f%%", m.InternetLossPct),
		Rating:  ratingWord(metrics.RateLoss(m.InternetLossPct / 100)),
		Meaning: lossMeaning(m.InternetLossPct),
	})

	if m.Bufferbloat != "" {
		out = append(out, Line{
			Label: "Bufferbloat", Value: m.Bufferbloat,
			Rating:  ratingWord(metrics.GradeRating(m.Bufferbloat)),
			Meaning: bufferbloatMeaning(m.Bufferbloat),
		})
	}

	// Path context (where the latency sits).
	out = append(out, Line{Label: "Gateway (LAN)", Value: ms(m.GatewayMs), Rating: "INFO",
		Meaning: "round-trip to your own router — your side of the link"})
	out = append(out, Line{Label: "ISP hop", Value: ms(m.ISPMs), Rating: "INFO",
		Meaning: "round-trip to your ISP's edge — the first hop outside your home"})

	out = append(out, Line{Label: "DNS", Value: ms(m.DNSms),
		Rating:  dnsRating(m.DNSms),
		Meaning: dnsMeaning(m.DNSms)})

	// Local machine.
	out = append(out, Line{Label: "CPU", Value: fmt.Sprintf("%.0f%%", m.CPUPct),
		Rating: loadRating(m.CPUPct), Meaning: cpuMeaning(m.CPUPct)})
	out = append(out, Line{Label: "Memory", Value: fmt.Sprintf("%.0f%% (%.1f/%.1f GB)", m.MemPct, m.MemUsedGB, m.MemTotalGB),
		Rating: loadRating(m.MemPct), Meaning: memMeaning(m.MemPct)})
	if m.GPUPct >= 0 {
		out = append(out, Line{Label: "GPU", Value: fmt.Sprintf("%.0f%%", m.GPUPct),
			Rating: loadRating(m.GPUPct), Meaning: gpuMeaning(m.GPUPct)})
	}
	if m.OnWiFi {
		out = append(out, Line{Label: "Wi-Fi signal", Value: fmt.Sprintf("%d dBm", m.RSSI),
			Rating: rssiRating(m.RSSI), Meaning: rssiMeaning(m.RSSI)})
	}
	return out
}

// Summary returns a workload-aware, plain-language explanation of what the issue
// means for the user, based on the identified culprit.
func Summary(is model.Issue) string {
	switch is.Culprit {
	case model.CulpritLocalMachine:
		return "Your PC — not the network — is the bottleneck right now. A saturated CPU/NIC delays packet and I/O handling, so everything feels laggy: input in games, frames in calls, and throughput for downloads and AI jobs. Freeing up local resources should restore responsiveness."
	case model.CulpritWiFi:
		return "The weak point is the Wi-Fi link between you and the router — before your traffic even leaves the house. A weak or unstable signal injects jitter and loss, which shows up as stutter and rubber-banding in real-time apps and as slow, bursty transfers for large downloads and AI syncs. A wired connection would remove it."
	case model.CulpritLANRouter:
		return "The problem is inside your home network — the router, a cable, or a switch — since round-trips to your own gateway are already slow or lossy. Everything downstream inherits it. This is usually fixable locally (reboot/replace the router or cabling)."
	case model.CulpritISPAccess:
		return "Your local network is healthy, but your ISP access link is the bottleneck — most often bufferbloat: latency balloons whenever the link is busy (a download, a backup, a training job). Real-time work degrades exactly when you're also moving data. Smart Queue Management on the router fixes this; more bandwidth does not."
	case model.CulpritUpstream:
		return "Your machine, LAN and ISP edge all look fine — the degradation is upstream, in the wider internet or on the path to the specific server/service you're reaching. This is usually outside your control (transient peering/route congestion); a different server region or route can help."
	case model.CulpritDNS:
		return "Connectivity itself is fine, but name resolution is slow, so the lag shows up when first connecting to servers, websites, and APIs rather than during steady transfers. Switching to a fast resolver (1.1.1.1 / 8.8.8.8) typically fixes it."
	default:
		if is.Detail != "" {
			return is.Detail
		}
		return "Connection and machine look healthy."
	}
}

// --- per-metric meaning text ---

func d(milli float64) time.Duration { return time.Duration(milli * float64(time.Millisecond)) }

func latencyMeaning(v float64) string {
	switch {
	case v <= 0:
		return "no measurement"
	case v < 20:
		return "feels instant — great for competitive play, real-time control, and tight inference loops"
	case v < 50:
		return "responsive for almost everything"
	case v < 100:
		return "a noticeable delay — fine for most work, sluggish for twitch games and tight control loops"
	default:
		return "laggy — expect delays in games, calls, and interactive/remote sessions"
	}
}

func jitterMeaning(v float64) string {
	switch {
	case v <= 0:
		return "no measurement"
	case v < 5:
		return "very stable timing"
	case v < 15:
		return "minor variation — generally fine"
	case v < 30:
		return "noticeable — occasional stutter, jitter buffers grow, uneven throughput"
	default:
		return "erratic timing — stutter, rubber-banding, choppy audio/video, bursty transfers"
	}
}

func lossMeaning(pct float64) string {
	switch {
	case pct < 0.1:
		return "effectively no loss"
	case pct < 1:
		return "low — generally unnoticeable"
	case pct < 2.5:
		return "elevated — occasional stalls and retransmits"
	default:
		return "high — frequent lost packets: visible stalls, retransmits, degraded throughput, and slow AI collective ops"
	}
}

func bufferbloatMeaning(grade string) string {
	switch grade {
	case "A+", "A":
		return "latency barely rises under load — excellent"
	case "B":
		return "small latency rise under load — fine"
	case "C":
		return "marginal — latency climbs under load; calls/games suffer when the link is busy"
	case "D":
		return "poor — large latency spikes under load (downloads, backups, training jobs)"
	default: // F
		return "bad — latency explodes under load; the link is unusable for real-time work while busy"
	}
}

func dnsRating(v float64) string {
	switch {
	case v <= 0:
		return "INFO"
	case v < 50:
		return "GOOD"
	case v < 100:
		return "FAIR"
	default:
		return "POOR"
	}
}

func dnsMeaning(v float64) string {
	switch {
	case v <= 0:
		return "no measurement"
	case v < 50:
		return "fast name resolution"
	case v < 100:
		return "slightly slow to connect to servers/sites/APIs"
	default:
		return "slow — connecting to servers, sites, and APIs feels laggy"
	}
}

func loadRating(pct float64) string {
	switch {
	case pct >= 90:
		return "POOR"
	case pct >= 75:
		return "FAIR"
	default:
		return "GOOD"
	}
}

func cpuMeaning(pct float64) string {
	switch {
	case pct >= 90:
		return "saturated — the machine itself is the bottleneck and will throttle workloads"
	case pct >= 75:
		return "high — little headroom; spikes may cause hitches"
	default:
		return "healthy headroom"
	}
}

func memMeaning(pct float64) string {
	switch {
	case pct >= 90:
		return "under pressure — paging likely, which stalls everything"
	case pct >= 75:
		return "high usage"
	default:
		return "healthy headroom"
	}
}

func gpuMeaning(pct float64) string {
	switch {
	case pct >= 90:
		return "maxed — GPU-bound (rendering or AI compute)"
	case pct >= 75:
		return "high utilization"
	default:
		return "headroom available"
	}
}

func rssiRating(dBm int) string {
	switch {
	case dBm >= -60:
		return "GOOD"
	case dBm >= -67:
		return "FAIR"
	default:
		return "POOR"
	}
}

func rssiMeaning(dBm int) string {
	switch {
	case dBm >= -60:
		return "strong signal"
	case dBm >= -67:
		return "OK — about the minimum for stable real-time use"
	case dBm >= -75:
		return "weak — expect jitter and loss"
	default:
		return "very weak — unreliable; move closer or use Ethernet"
	}
}
