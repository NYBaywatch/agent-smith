package classifier

import (
	"testing"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/bufferbloat"
	"github.com/NYBaywatch/agent-smith/internal/dnsprobe"
	"github.com/NYBaywatch/agent-smith/internal/metrics"
	"github.com/NYBaywatch/agent-smith/internal/model"
	"github.com/NYBaywatch/agent-smith/internal/netinfo"
	"github.com/NYBaywatch/agent-smith/internal/sysinfo"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func stat(mean, jitter time.Duration, loss float64) metrics.Stats {
	return metrics.Stats{Mean: mean, P95: mean, Jitter: jitter, Loss: loss, Recv: 30, Sent: 30}
}

func target(name string, role model.Role, mean, jitter time.Duration, loss float64) *model.TargetStats {
	return &model.TargetStats{Name: name, Role: role, Stats: stat(mean, jitter, loss), Alive: true}
}

// healthy returns a baseline snapshot where everything is good (wired).
func healthy() model.Snapshot {
	return model.Snapshot{
		Time:    time.Now(),
		Gateway: target("Gateway", model.RoleGateway, ms(1), ms(0), 0),
		ISPHop:  target("ISP hop", model.RoleISPHop, ms(9), ms(1), 0),
		Internet: []model.TargetStats{
			*target("Cloudflare", model.RoleInternet, ms(18), ms(2), 0),
			*target("Google", model.RoleInternet, ms(20), ms(3), 0),
		},
		Net: &netinfo.Info{
			GatewayIP: nil,
			Active:    &netinfo.Interface{Name: "Ethernet", Media: netinfo.MediaWired, Up: true, LinkMbps: 1000},
		},
		Sys: sysinfo.Stats{CPUPercent: 10, MemPercent: 40},
		DNS: dnsprobe.Result{Avg: ms(20), Lookups: 3},
	}
}

func TestHealthy(t *testing.T) {
	s := healthy()
	v := Classify(s)
	if v.Culprit != model.CulpritHealthy {
		t.Fatalf("got %v (%q), want Healthy", v.Culprit, v.Headline)
	}
}

func TestLocalMachineCPU(t *testing.T) {
	s := healthy()
	s.Sys.CPUPercent = 96
	v := Classify(s)
	if v.Culprit != model.CulpritLocalMachine {
		t.Fatalf("got %v (%q), want LocalMachine", v.Culprit, v.Headline)
	}
}

func TestLocalMachineNICErrors(t *testing.T) {
	s := healthy()
	s.Net.Active.InErrors = 500
	v := Classify(s)
	if v.Culprit != model.CulpritLocalMachine {
		t.Fatalf("got %v (%q), want LocalMachine (NIC)", v.Culprit, v.Headline)
	}
}

func TestWiFiWeakSignal(t *testing.T) {
	s := healthy()
	s.Net.Active.Media = netinfo.MediaWireless
	s.Net.WiFi = &netinfo.WiFi{SSID: "Home", RSSI: -82, SignalQuality: 36}
	v := Classify(s)
	if v.Culprit != model.CulpritWiFi {
		t.Fatalf("got %v (%q), want WiFi", v.Culprit, v.Headline)
	}
	if v.Severity != model.SevCritical {
		t.Fatalf("RSSI -82 should be critical, got %v", v.Severity)
	}
}

func TestWiFiMarginalWithBadGateway(t *testing.T) {
	s := healthy()
	s.Net.Active.Media = netinfo.MediaWireless
	s.Net.WiFi = &netinfo.WiFi{SSID: "Home", RSSI: -69, SignalQuality: 62}
	s.Gateway = target("Gateway", model.RoleGateway, ms(25), ms(12), 0.02) // bad LAN over wifi
	v := Classify(s)
	if v.Culprit != model.CulpritWiFi {
		t.Fatalf("got %v (%q), want WiFi (marginal+bad gw)", v.Culprit, v.Headline)
	}
}

func TestLANRouterWired(t *testing.T) {
	s := healthy()
	// Wired, gateway RTT/jitter high → LAN/router fault.
	s.Gateway = target("Gateway", model.RoleGateway, ms(18), ms(10), 0.0)
	v := Classify(s)
	if v.Culprit != model.CulpritLANRouter {
		t.Fatalf("got %v (%q), want LAN/router", v.Culprit, v.Headline)
	}
}

func TestISPBufferbloat(t *testing.T) {
	s := healthy()
	s.Bufferbloat = &bufferbloat.Result{IdleRTT: ms(18), LoadedRTT: ms(420), Added: ms(402), Grade: "F"}
	v := Classify(s)
	if v.Culprit != model.CulpritISPAccess {
		t.Fatalf("got %v (%q), want ISP access (bufferbloat)", v.Culprit, v.Headline)
	}
}

func TestUpstreamInternet(t *testing.T) {
	s := healthy()
	// LAN + ISP hop fine, but anchors poor.
	s.Internet = []model.TargetStats{
		*target("Cloudflare", model.RoleInternet, ms(180), ms(40), 0.04),
		*target("Google", model.RoleInternet, ms(190), ms(45), 0.05),
	}
	v := Classify(s)
	if v.Culprit != model.CulpritUpstream {
		t.Fatalf("got %v (%q), want Upstream", v.Culprit, v.Headline)
	}
}

func TestDNSSlow(t *testing.T) {
	s := healthy()
	s.DNS.Avg = ms(300)
	v := Classify(s)
	if v.Culprit != model.CulpritDNS {
		t.Fatalf("got %v (%q), want DNS", v.Culprit, v.Headline)
	}
}

func TestLocalMaskingPrecedence(t *testing.T) {
	// CPU saturation must win even if the internet also looks bad.
	s := healthy()
	s.Sys.CPUPercent = 99
	s.Internet = []model.TargetStats{*target("Cloudflare", model.RoleInternet, ms(300), ms(60), 0.1)}
	v := Classify(s)
	if v.Culprit != model.CulpritLocalMachine {
		t.Fatalf("got %v, want LocalMachine to mask internet", v.Culprit)
	}
}
