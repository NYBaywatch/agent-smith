package interpret

import (
	"strings"
	"testing"

	"github.com/NYBaywatch/agent-smith/internal/model"
)

func TestLinesRatings(t *testing.T) {
	m := model.IssueMetrics{
		InternetMs: 95, InternetJitterMs: 35, InternetLossPct: 4.2,
		Bufferbloat: "D", GatewayMs: 0.6, ISPMs: 60, DNSms: 12,
		CPUPct: 95, MemPct: 50, MemUsedGB: 16, MemTotalGB: 32,
		GPUPct: 80, OnWiFi: true, RSSI: -82,
	}
	lines := Lines(m)
	got := map[string]Line{}
	for _, l := range lines {
		got[l.Label] = l
	}

	checks := []struct {
		label, rating string
	}{
		{"Latency", "FAIR"},      // 95ms -> playable
		{"Jitter", "POOR"},       // 35ms -> poor
		{"Packet loss", "POOR"},  // 4.2% -> poor
		{"Bufferbloat", "POOR"},  // D -> poor
		{"DNS", "GOOD"},          // 12ms -> good
		{"CPU", "POOR"},          // 95% -> poor
		{"GPU", "FAIR"},          // 80% -> fair
		{"Wi-Fi signal", "POOR"}, // -82 dBm -> poor
	}
	for _, c := range checks {
		l, ok := got[c.label]
		if !ok {
			t.Errorf("missing line %q", c.label)
			continue
		}
		if l.Rating != c.rating {
			t.Errorf("%s rating = %q, want %q", c.label, l.Rating, c.rating)
		}
		if l.Meaning == "" {
			t.Errorf("%s has empty meaning", c.label)
		}
	}
}

func TestGPUOmittedWhenUnavailable(t *testing.T) {
	lines := Lines(model.IssueMetrics{GPUPct: -1})
	for _, l := range lines {
		if l.Label == "GPU" {
			t.Fatal("GPU line should be omitted when GPUPct < 0")
		}
	}
}

func TestSummaryPerCulprit(t *testing.T) {
	for _, c := range []model.Culprit{
		model.CulpritLocalMachine, model.CulpritWiFi, model.CulpritLANRouter,
		model.CulpritISPAccess, model.CulpritUpstream, model.CulpritDNS,
	} {
		s := Summary(model.Issue{Culprit: c})
		if len(s) < 40 {
			t.Errorf("culprit %v summary too short: %q", c, s)
		}
	}
	// Bufferbloat summary should mention SQM (the real fix).
	if s := Summary(model.Issue{Culprit: model.CulpritISPAccess}); !strings.Contains(s, "Queue") {
		t.Errorf("ISP summary should mention Smart Queue Management: %q", s)
	}
}
