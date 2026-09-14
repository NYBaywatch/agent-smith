package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/baseline"
	"github.com/NYBaywatch/agent-smith/internal/incident"
	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/model"
	"github.com/NYBaywatch/agent-smith/internal/netinfo"
	"github.com/NYBaywatch/agent-smith/internal/pathmon"
	"github.com/NYBaywatch/agent-smith/internal/synth"
	"github.com/NYBaywatch/agent-smith/internal/sysinfo"
)

func msd(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func target(name, host string, role model.Role, mean, jitter time.Duration, loss float64) *model.TargetStats {
	return &model.TargetStats{Name: name, Host: host, Role: role, Alive: true,
		Stats: metrics.Stats{Mean: mean, P95: mean + msd(2), Jitter: jitter, Loss: loss, Sent: 15, Recv: 15}}
}

func service(name string, runs, consecutive int, lastOK bool, ttfb time.Duration) synth.Summary {
	return synth.Summary{
		Check: synth.Check{Name: name, URL: "https://" + strings.ToLower(name) + ".test/", Category: synth.CategorySaaS, Provider: "P"},
		Stats: synth.Stats{Runs: runs, Consecutive: consecutive, Availability: 0.9, MeanTTFB: ttfb, Mean: ttfb + msd(50),
			Last: synth.Result{OK: lastOK, Status: 200, Edge: "Cloudflare EWR", When: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)}},
	}
}

func healthy() model.Snapshot {
	now := time.Date(2026, 9, 14, 12, 30, 0, 0, time.UTC)
	return model.Snapshot{
		Time:    now,
		Gateway: target("Gateway", "192.168.1.1", model.RoleGateway, msd(1), 0, 0),
		ISPHop:  target("ISP hop", "67.59.1.1", model.RoleISPHop, msd(9), msd(1), 0),
		Internet: []model.TargetStats{
			*target("Cloudflare", "1.1.1.1", model.RoleInternet, msd(18), msd(2), 0),
			*target("Google", "8.8.8.8", model.RoleInternet, msd(12), msd(3), 0.005),
		},
		Net:     &netinfo.Info{Active: &netinfo.Interface{Name: "Ethernet", Media: netinfo.MediaWired, Up: true, LinkMbps: 1000, MTU: 1500}},
		Sys:     sysinfo.Stats{CPUPercent: 10, MemPercent: 40, GPUPercent: -1},
		Verdict: model.Verdict{Culprit: model.CulpritHealthy, Severity: model.SevOK, Confidence: 0.9, Headline: "Connection is healthy"},
		Synthetics: []synth.Summary{
			service("Pending", 0, 0, false, 0),
			service("Down", 5, 3, false, msd(50)),
			service("Fail", 5, 1, false, msd(50)),
			service("Slow", 5, 0, true, msd(900)),
			service("Ok", 5, 0, true, msd(40)),
		},
		Paths: []pathmon.Path{{Name: "Cloudflare", Dest: "1.1.1.1", When: now, Reached: true, Hops: []pathmon.Hop{
			{TTL: 1, Addr: "192.168.1.1", Private: true, Avg: msd(1), Max: msd(1)},
			{TTL: 2, Addr: "67.59.1.1", ASN: 6128, ASName: "CABLE-NET-1", Avg: msd(9), Max: msd(10)},
			{TTL: 3, Addr: "1.1.1.1", ASN: 13335, ASName: "CLOUDFLARENET", Avg: msd(18), Max: msd(20), Loss: 0.1},
		}}},
		PathDiag: pathmon.Diagnosis{HopIndex: 1, Reason: "latency jumps", Segment: pathmon.SegmentISP},
		SLA: []model.SLAEntry{{Key: "internet", Name: "Internet", Kind: "ping",
			Hour: baseline.Summary{Availability: 1, Samples: 60}, Day: baseline.Summary{Availability: 0.999, Samples: 1000, P95: 25, Mean: 15},
			Week: baseline.Summary{Availability: 0.998}, Baseline: baseline.Baseline{Median: 14, Valid: true},
			Compliance: baseline.Compliance{OK: true, AvailabilityOK: true, LatencyOK: true, ErrorBudgetLeft: 0.5},
			CurrentMs:  12, Z: 0.3}},
		Incident:       &model.IncidentRef{ID: 3, Start: now.Add(-90 * time.Second), Culprit: model.CulpritUpstream, Peak: model.SevDegraded, Ticks: 90, Headline: "Problem is upstream"},
		AlertRaw:       90,
		AlertIncidents: 1,
	}
}

func TestBuildSnapshotRingsAndHeadline(t *testing.T) {
	s := BuildSnapshot(healthy(), baseline.SLO{Availability: 0.999, P95Ms: 100})
	want := []string{"LAN", "ISP", "NET", "NET"}
	if len(s.Rings) != len(want) {
		t.Fatalf("rings = %d, want %d", len(s.Rings), len(want))
	}
	for i, w := range want {
		if s.Rings[i].Ring != w {
			t.Errorf("ring %d = %s, want %s", i, s.Rings[i].Ring, w)
		}
	}
	if s.Rings[0].MeanMs != 1 || s.Rings[0].Rating != "Excellent" {
		t.Errorf("LAN ring = %+v", s.Rings[0])
	}
	// Healthiest anchor is Google at 12 ms.
	if s.Headline.LatencyMs != 12 || s.Headline.LatencyRating != "Excellent" {
		t.Errorf("headline = %+v", s.Headline)
	}
	if s.Headline.LossPct != 0.5 || s.Headline.LossRating != "Good" {
		t.Errorf("loss = %v %s", s.Headline.LossPct, s.Headline.LossRating)
	}
	if s.Headline.Bufferbloat != nil {
		t.Error("bufferbloat should be nil when not measured")
	}
	if s.SLO.AvailabilityPct != 99.9 || s.SLO.P95Ms != 100 {
		t.Errorf("slo = %+v", s.SLO)
	}
	if s.Verdict.SeverityName != "OK" || s.Verdict.Culprit != "Healthy" {
		t.Errorf("verdict = %+v", s.Verdict)
	}
	if s.Net == nil || s.Net.Interface != "Ethernet" || s.Net.Media != "Wired" || s.Net.WiFi != nil {
		t.Errorf("net = %+v", s.Net)
	}
	if s.Incident == nil || s.Incident.ID != 3 || s.Incident.DurationS != 90 || s.Incident.PeakName != "Degraded" {
		t.Errorf("incident = %+v", s.Incident)
	}
	if s.Alerts.Raw != 90 || s.Alerts.Incidents != 1 {
		t.Errorf("alerts = %+v", s.Alerts)
	}
}

func TestBuildSnapshotServiceStatuses(t *testing.T) {
	s := BuildSnapshot(healthy(), baseline.SLO{})
	want := map[string]string{"Pending": "pending", "Down": "down", "Fail": "fail", "Slow": "slow", "Ok": "ok"}
	if len(s.Services) != len(want) {
		t.Fatalf("services = %d", len(s.Services))
	}
	for _, sv := range s.Services {
		if sv.Status != want[sv.Name] {
			t.Errorf("%s: status %s, want %s", sv.Name, sv.Status, want[sv.Name])
		}
		if sv.Name == "Ok" {
			if sv.TTFBMs != 40 || sv.TotalMs != 90 || sv.AvailPct != 90 || sv.Edge != "Cloudflare EWR" || sv.LastAt == "" {
				t.Errorf("ok service = %+v", sv)
			}
		}
		if sv.Name == "Pending" && (sv.LastAt != "" || sv.TotalMs != 0) {
			t.Errorf("pending service should carry no timings: %+v", sv)
		}
	}
}

func TestBuildSnapshotPathsAndSLA(t *testing.T) {
	s := BuildSnapshot(healthy(), baseline.SLO{})
	if len(s.Paths) != 1 {
		t.Fatalf("paths = %d", len(s.Paths))
	}
	p := s.Paths[0]
	if p.ASPath != "AS6128>AS13335" || !p.Reached {
		t.Errorf("path = %+v", p)
	}
	segs := []string{"LAN", "ISP", "Destination"}
	for i, h := range p.Hops {
		if h.Segment != segs[i] {
			t.Errorf("hop %d segment = %s, want %s", i, h.Segment, segs[i])
		}
		if h.Degraded != (i == 1) {
			t.Errorf("hop %d degraded = %v", i, h.Degraded)
		}
	}
	if p.Hops[2].LossPct != 10 || p.Diagnosis.HopIndex != 1 || p.Diagnosis.Segment != "ISP" {
		t.Errorf("hop/diag = %+v %+v", p.Hops[2], p.Diagnosis)
	}
	if len(s.SLA) != 1 {
		t.Fatalf("sla = %d", len(s.SLA))
	}
	e := s.SLA[0]
	if e.Avail24hPct != 99.9 || e.Avail1hPct != 100 || e.BudgetLeftPct != 50 || !e.SLOOK || e.BaselineMs != 14 || !e.BaselineValid || e.P9524hMs != 25 {
		t.Errorf("sla = %+v", e)
	}
}

func TestBuildSnapshotZeroIsNilSafe(t *testing.T) {
	s := BuildSnapshot(model.Snapshot{}, baseline.SLO{})
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	js := string(data)
	for _, key := range []string{`"rings":[]`, `"services":[]`, `"paths":[]`, `"route_changes":[]`, `"sla":[]`, `"servers":[]`, `"authoritative":[]`} {
		if !strings.Contains(js, key) {
			t.Errorf("missing empty slice %s in %s", key, js)
		}
	}
	if s.Net != nil || s.Connection != nil || s.BGP != nil || s.Incident != nil {
		t.Error("optional sections should be nil")
	}
	if s.Headline.LatencyRating != "—" {
		t.Errorf("rating = %q", s.Headline.LatencyRating)
	}
}

func TestJSONKeys(t *testing.T) {
	data, err := json.Marshal(BuildSnapshot(healthy(), baseline.SLO{}))
	if err != nil {
		t.Fatal(err)
	}
	js := string(data)
	for _, key := range []string{`"mean_ms"`, `"avail_pct"`, `"hop_index"`, `"severity_name"`, `"budget_left_pct"`, `"as_path"`} {
		if !strings.Contains(js, key) {
			t.Errorf("missing key %s", key)
		}
	}
}

func TestBuildHistoryDownsamples(t *testing.T) {
	base := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	var h []model.HistPoint
	for i := 0; i < 100; i++ {
		h = append(h, model.HistPoint{T: base.Add(time.Duration(i) * time.Second), GwMs: 1, IspMs: 0, NetMs: float64(i + 1)})
	}
	out := BuildHistory(h, 10)
	if len(out) != 10 {
		t.Fatalf("len = %d", len(out))
	}
	// First bucket averages 1..10 → 5.5; ISP is all zeros → 0.
	if out[0].Net != 5.5 || out[0].Isp != 0 || out[0].Gw != 1 {
		t.Errorf("bucket 0 = %+v", out[0])
	}
	if out[9].Net != 95.5 {
		t.Errorf("bucket 9 = %+v", out[9])
	}
	if out[0].T >= out[9].T {
		t.Error("expected oldest first")
	}
	if got := BuildHistory(h[:5], 10); len(got) != 5 {
		t.Errorf("no downsampling needed: len = %d", len(got))
	}
	if got := BuildHistory(nil, 10); got == nil || len(got) != 0 {
		t.Error("nil input should give empty non-nil slice")
	}
}

func TestBuildIssuesNewestFirstWithLines(t *testing.T) {
	list := []model.Issue{
		{Time: time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC), Severity: model.SevDegraded, Culprit: model.CulpritWiFi, Headline: "old",
			Metrics: model.IssueMetrics{InternetMs: 90, GPUPct: -1}, Procs: []model.ProcInfo{{PID: 1, Name: "x"}}},
		{Time: time.Date(2026, 9, 14, 11, 0, 0, 0, time.UTC), Severity: model.SevCritical, Culprit: model.CulpritUpstream, Headline: "new",
			Metrics: model.IssueMetrics{InternetMs: 300, GPUPct: -1}},
	}
	out := BuildIssues(list)
	if len(out) != 2 || out[0].Headline != "new" || out[1].Headline != "old" {
		t.Fatalf("order wrong: %+v", out)
	}
	if out[0].SeverityName != "Critical" || out[0].Culprit != "Upstream internet" {
		t.Errorf("names = %s %s", out[0].SeverityName, out[0].Culprit)
	}
	if len(out[0].Lines) == 0 || out[0].Lines[0].Label != "Latency" || out[0].Summary == "" {
		t.Errorf("lines/summary missing: %+v", out[0])
	}
	if len(out[1].Procs) != 1 || len(out[0].Procs) != 0 {
		t.Errorf("procs = %d %d", len(out[1].Procs), len(out[0].Procs))
	}
}

func TestBuildIncidents(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	list := []incident.Incident{
		{ID: 2, Start: now.Add(-30 * time.Second), Culprit: model.CulpritISPAccess, Peak: model.SevDegraded, Ticks: 30, Headline: "open one", Culprits: []string{"ISP access link"}},
		{ID: 1, Start: now.Add(-time.Hour), End: now.Add(-50 * time.Minute), Culprit: model.CulpritWiFi, Peak: model.SevCritical, Ticks: 600, Headline: "closed one"},
	}
	out := BuildIncidents(list, now)
	if len(out) != 2 {
		t.Fatal(len(out))
	}
	if !out[0].Open || out[0].End != "" || out[0].DurationS != 30 || out[0].PeakName != "Degraded" || len(out[0].Culprits) != 1 {
		t.Errorf("open = %+v", out[0])
	}
	if out[1].Open || out[1].End == "" || out[1].DurationS != 600 || out[1].Culprits == nil {
		t.Errorf("closed = %+v", out[1])
	}
}
