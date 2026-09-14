package classifier

import (
	"fmt"
	"strings"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/model"
	"github.com/NYBaywatch/agent-smith/internal/pathmon"
)

// applyIPM refines a verdict with the internet-performance signals: the
// hop-by-hop path diagnosis (topology-aware probable cause), synthetic HTTP
// checks (service-side failures while the path is fine), the 24 h latency
// baseline (anomaly detection), and BGP visibility of the user's own prefix.
// It runs after the core most-local-first rules so a local fault still wins.
func applyIPM(s model.Snapshot, v model.Verdict, t Thresholds) model.Verdict {
	v = refineWithPath(s, v)

	// Path is fine (nothing at/above Degraded): look for service-side trouble.
	if v.Severity < model.SevDegraded {
		if sv, ok := remoteServiceVerdict(s); ok {
			return sv
		}
	}

	// Healthy but above the machine's own normal: flag as Watch.
	if v.Culprit == model.CulpritHealthy {
		if e, ok := internetAnomaly(s); ok {
			v.Severity = model.SevWatch
			v.Headline = fmt.Sprintf("Latency above your normal baseline (%.0f ms vs %.0f ms typical)", e.CurrentMs, e.Baseline.Median)
			v.Detail = fmt.Sprintf("Nothing is failing, but internet RTT is %.1f robust standard deviations above the median of the last 24 h on this connection — an early sign of congestion or a route change. %s", e.Z, v.Detail)
			v.Confidence = 0.6
		}
	}

	if s.BGP != nil && !s.BGP.Healthy() && s.BGP.Announced {
		v.Detail = strings.TrimSpace(v.Detail + fmt.Sprintf(" BGP: your prefix %s is seen by only %.0f%% of route collectors — reachability from parts of the internet may be impaired.", s.BGP.Prefix, s.BGP.Visibility*100))
		if v.Severity < model.SevWatch {
			v.Severity = model.SevWatch
		}
	}
	return v
}

// refineWithPath uses the traceroute diagnosis to say *where* upstream/ISP
// degradation begins, and re-attributes an "upstream" verdict to the ISP or LAN
// when the first degraded hop lives there.
func refineWithPath(s model.Snapshot, v model.Verdict) model.Verdict {
	d := s.PathDiag
	if d.HopIndex < 0 || len(s.Paths) == 0 {
		return v
	}
	if v.Culprit != model.CulpritUpstream && v.Culprit != model.CulpritISPAccess {
		return v
	}
	hop := hopAt(s.Paths, d.HopIndex)
	if hop == nil {
		return v
	}
	where := fmt.Sprintf("hop %d", hop.TTL)
	if hop.Addr != "" {
		where += " " + hop.Addr
	}
	if hop.ASName != "" {
		where += fmt.Sprintf(" (AS%d %s)", hop.ASN, hop.ASName)
	} else if hop.ASN > 0 {
		where += fmt.Sprintf(" (AS%d)", hop.ASN)
	}
	v.Detail = strings.TrimSpace(v.Detail + fmt.Sprintf(" Hop-by-hop: %s at %s, in the %s segment.", d.Reason, where, d.Segment))

	if v.Culprit == model.CulpritUpstream {
		switch d.Segment {
		case pathmon.SegmentISP:
			v.Culprit = model.CulpritISPAccess
			v.Headline = "ISP network is where the degradation starts"
			v.Fix = "The traceroute shows the problem inside your ISP's network — report it with the hop details; it is not your LAN or the remote service."
			v.Confidence = 0.75
		case pathmon.SegmentLAN:
			v.Culprit = model.CulpritLANRouter
			v.Headline = "Degradation starts inside your local network"
			v.Confidence = 0.7
		default:
			v.Confidence = 0.7
		}
	}
	return v
}

func hopAt(paths []pathmon.Path, idx int) *pathmon.Hop {
	p := paths[0]
	if idx < 0 || idx >= len(p.Hops) {
		return nil
	}
	return &p.Hops[idx]
}

// remoteServiceVerdict reports synthetic checks that are down while the network
// path itself is fine: the classic "it's them, not you" answer.
func remoteServiceVerdict(s model.Snapshot) (model.Verdict, bool) {
	var down []string
	var total int
	for _, sm := range s.Synthetics {
		if sm.Stats.Runs == 0 {
			continue
		}
		total++
		if sm.Stats.Down() {
			down = append(down, sm.Check.Name)
		}
	}
	if len(down) == 0 {
		return model.Verdict{}, false
	}
	if total >= 3 && len(down) == total {
		// Ping works but no HTTP check succeeds: something between the browser
		// and the internet is interfering at the HTTP/TLS layer.
		return model.Verdict{
			Culprit:    model.CulpritLANRouter,
			Severity:   model.SevDegraded,
			Confidence: 0.6,
			Headline:   "Web traffic is failing even though ping works",
			Detail:     fmt.Sprintf("All %d HTTP checks are failing while ICMP to the internet is fine — this pattern usually means a captive portal, proxy, firewall/security software, a broken DNS setup, or a system clock far enough off to fail TLS.", total),
			Fix:        "Open a browser to see if a sign-in page appears; check proxy settings, security software, DNS, and the system clock.",
		}, true
	}
	sev := model.SevWatch
	if len(down) >= 3 {
		sev = model.SevDegraded
	}
	list := strings.Join(down, ", ")
	if len(down) > 4 {
		list = strings.Join(down[:4], ", ") + fmt.Sprintf(" +%d more", len(down)-4)
	}
	head := fmt.Sprintf("%s is failing (service-side)", down[0])
	if len(down) > 1 {
		head = fmt.Sprintf("%d services failing (service-side)", len(down))
	}
	return model.Verdict{
		Culprit:    model.CulpritRemoteService,
		Severity:   sev,
		Confidence: 0.75,
		Headline:   head,
		Detail:     fmt.Sprintf("Your gateway, ISP edge and internet anchors are healthy, but these synthetic checks keep failing: %s. The fault is on the remote service or its provider (CDN/cloud), not your connection.", list),
		Fix:        "Nothing to fix locally — check the provider's status page; if it is your own service, look at its hosting/CDN.",
	}, true
}

// internetAnomaly returns the SLA entry for the internet ring when it is
// currently anomalous against its 24 h baseline.
func internetAnomaly(s model.Snapshot) (model.SLAEntry, bool) {
	for _, e := range s.SLA {
		if e.Key == "internet" && e.Anomalous && e.CurrentMs > 0 {
			return e, true
		}
	}
	return model.SLAEntry{}, false
}

// PathThresholds are the loss / RTT-jump limits used when reading a traceroute.
const (
	PathLossThreshold = 0.05
	PathJumpThreshold = 40 * time.Millisecond
)
