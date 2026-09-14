package classifier

import (
	"strings"
	"testing"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/baseline"
	"github.com/NYBaywatch/agent-smith/internal/bgp"
	"github.com/NYBaywatch/agent-smith/internal/model"
	"github.com/NYBaywatch/agent-smith/internal/pathmon"
	"github.com/NYBaywatch/agent-smith/internal/synth"
)

func synthSummary(name string, consecutive, runs int) synth.Summary {
	return synth.Summary{
		Check: synth.Check{Name: name, URL: "https://" + strings.ToLower(name) + ".test/", Category: synth.CategorySaaS},
		Stats: synth.Stats{Runs: runs, Consecutive: consecutive, Availability: 1},
	}
}

func TestRemoteServiceDownWhilePathHealthy(t *testing.T) {
	s := healthy()
	s.Synthetics = []synth.Summary{synthSummary("GitHub", 3, 5), synthSummary("Slack", 0, 5), synthSummary("Zoom", 0, 5)}
	v := Classify(s)
	if v.Culprit != model.CulpritRemoteService {
		t.Fatalf("culprit = %v, want RemoteService (%s)", v.Culprit, v.Headline)
	}
	if v.Severity != model.SevWatch {
		t.Errorf("one service down should be Watch, got %v", v.Severity)
	}
	if !strings.Contains(v.Headline, "GitHub") {
		t.Errorf("headline should name the service: %q", v.Headline)
	}
}

func TestManyServicesDownEscalates(t *testing.T) {
	s := healthy()
	s.Synthetics = []synth.Summary{synthSummary("A", 2, 5), synthSummary("B", 2, 5), synthSummary("C", 2, 5), synthSummary("D", 0, 5)}
	v := Classify(s)
	if v.Culprit != model.CulpritRemoteService || v.Severity != model.SevDegraded {
		t.Fatalf("got %v/%v: %s", v.Culprit, v.Severity, v.Headline)
	}
}

func TestAllHTTPFailingWhilePingWorksBlamesLocalLayer(t *testing.T) {
	s := healthy()
	s.Synthetics = []synth.Summary{synthSummary("A", 2, 5), synthSummary("B", 2, 5), synthSummary("C", 2, 5)}
	v := Classify(s)
	if v.Culprit != model.CulpritLANRouter {
		t.Fatalf("culprit = %v, want LANRouter (captive portal / proxy pattern): %s", v.Culprit, v.Headline)
	}
}

func TestLocalFaultStillWinsOverSynthetics(t *testing.T) {
	s := healthy()
	s.Sys.CPUPercent = 99
	s.Synthetics = []synth.Summary{synthSummary("GitHub", 3, 5)}
	if v := Classify(s); v.Culprit != model.CulpritLocalMachine {
		t.Fatalf("local rule should win, got %v", v.Culprit)
	}
}

func TestPathDiagnosisReattributesUpstreamToISP(t *testing.T) {
	s := healthy()
	// Internet anchors poor while LAN + ISP hop pings are fine → core says Upstream.
	s.Internet = []model.TargetStats{*target("Cloudflare", model.RoleInternet, ms(250), ms(5), 0)}
	s.Paths = []pathmon.Path{{Name: "Cloudflare", Dest: "1.1.1.1", Hops: []pathmon.Hop{
		{TTL: 1, Addr: "192.168.1.1", Private: true},
		{TTL: 2, Addr: "67.80.1.1", ASN: 6128, ASName: "CABLE-NET-1"},
		{TTL: 3, Addr: "67.80.9.9", ASN: 6128, ASName: "CABLE-NET-1"},
		{TTL: 4, Addr: "1.1.1.1", ASN: 13335, ASName: "CLOUDFLARENET"},
	}}}
	s.PathDiag = pathmon.Diagnosis{HopIndex: 2, Reason: "latency jumps by 230 ms", Segment: pathmon.SegmentISP}
	v := Classify(s)
	if v.Culprit != model.CulpritISPAccess {
		t.Fatalf("culprit = %v, want ISPAccess: %s", v.Culprit, v.Headline)
	}
	if !strings.Contains(v.Detail, "hop 3") || !strings.Contains(v.Detail, "CABLE-NET-1") {
		t.Errorf("detail should cite the hop and AS: %q", v.Detail)
	}
}

func TestPathDiagnosisIgnoredWhenClean(t *testing.T) {
	s := healthy()
	s.Internet = []model.TargetStats{*target("Cloudflare", model.RoleInternet, ms(250), ms(5), 0)}
	s.Paths = []pathmon.Path{{Dest: "1.1.1.1", Hops: []pathmon.Hop{{TTL: 1, Addr: "1.1.1.1"}}}}
	s.PathDiag = pathmon.Diagnosis{HopIndex: -1}
	if v := Classify(s); v.Culprit != model.CulpritUpstream {
		t.Fatalf("clean path should leave Upstream verdict, got %v", v.Culprit)
	}
}

func TestBaselineAnomalyRaisesWatch(t *testing.T) {
	s := healthy()
	s.SLA = []model.SLAEntry{{Key: "internet", CurrentMs: 60, Z: 9, Anomalous: true, Baseline: baseline.Baseline{Median: 18, MAD: 1, Valid: true}}}
	v := Classify(s)
	if v.Culprit != model.CulpritHealthy || v.Severity != model.SevWatch {
		t.Fatalf("got %v/%v: %s", v.Culprit, v.Severity, v.Headline)
	}
	if !strings.Contains(v.Headline, "baseline") {
		t.Errorf("headline should mention the baseline: %q", v.Headline)
	}
}

func TestBGPVisibilityNote(t *testing.T) {
	s := healthy()
	s.BGP = &bgp.Status{Prefix: "67.80.0.0/13", Announced: true, Visibility: 0.5, FetchedAt: time.Now()}
	v := Classify(s)
	if v.Severity < model.SevWatch || !strings.Contains(v.Detail, "BGP") {
		t.Fatalf("expected BGP watch note, got %v: %q", v.Severity, v.Detail)
	}
	s.BGP.Visibility = 1
	if v := Classify(s); v.Severity != model.SevOK {
		t.Errorf("healthy BGP should not change verdict, got %v", v.Severity)
	}
}
