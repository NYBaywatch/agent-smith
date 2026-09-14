package incident

import (
	"testing"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/model"
)

var t0 = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

func bad(c model.Culprit, sev model.Severity, head string) model.Verdict {
	return model.Verdict{Culprit: c, Severity: sev, Headline: head, Fix: "fix " + head}
}

var ok = model.Verdict{Culprit: model.CulpritHealthy, Severity: model.SevOK, Headline: "Connection is healthy"}

func TestSingleTickFlapOpens(t *testing.T) {
	g := NewGrouper(0, 0)
	ev := g.Observe(t0, bad(model.CulpritWiFi, model.SevDegraded, "weak wifi"))
	if ev.Opened == nil || ev.Closed != nil {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Opened.ID != 1 || ev.Opened.Ticks != 1 || !ev.Opened.Open() || ev.Opened.Culprit != model.CulpritWiFi {
		t.Errorf("opened = %+v", ev.Opened)
	}
	cur := g.Current()
	if cur == nil || cur.ID != 1 {
		t.Fatalf("current = %+v", cur)
	}
	// Mutating the copy must not affect the grouper.
	cur.Culprits[0] = "tampered"
	if g.Current().Culprits[0] != "Wi-Fi" {
		t.Error("Current returned a shared slice")
	}
}

func TestStormCompressesToOneIncident(t *testing.T) {
	g := NewGrouper(45*time.Second, 0)
	var opened, closed int
	for i := 0; i < 600; i++ {
		ev := g.Observe(t0.Add(time.Duration(i)*time.Second), bad(model.CulpritISPAccess, model.SevDegraded, "isp"))
		if ev.Opened != nil {
			opened++
		}
		if ev.Closed != nil {
			closed++
		}
	}
	if opened != 1 || closed != 0 {
		t.Fatalf("opened=%d closed=%d", opened, closed)
	}
	cur := g.Current()
	if cur.Ticks != 600 {
		t.Errorf("ticks = %d", cur.Ticks)
	}
	if raw, n := g.Stats(); raw != 600 || n != 1 {
		t.Errorf("stats = %d/%d", raw, n)
	}
	if cur.Duration(t0.Add(599*time.Second)) != 599*time.Second {
		t.Errorf("duration = %v", cur.Duration(t0.Add(599*time.Second)))
	}
}

func TestCooldownCloses(t *testing.T) {
	g := NewGrouper(45*time.Second, 0)
	g.Observe(t0, bad(model.CulpritLANRouter, model.SevDegraded, "lan"))
	g.Observe(t0.Add(10*time.Second), bad(model.CulpritLANRouter, model.SevCritical, "lan worse"))
	// Healthy, but not yet past the cooldown.
	if ev := g.Observe(t0.Add(30*time.Second), ok); ev.Closed != nil || g.Current() == nil {
		t.Fatal("closed before cooldown")
	}
	// A degraded tick inside the cooldown extends the incident.
	g.Observe(t0.Add(40*time.Second), bad(model.CulpritLANRouter, model.SevDegraded, "lan again"))
	if ev := g.Observe(t0.Add(80*time.Second), ok); ev.Closed != nil {
		t.Fatal("cooldown should restart after a degraded tick")
	}
	ev := g.Observe(t0.Add(85*time.Second), ok)
	if ev.Closed == nil || ev.Opened != nil {
		t.Fatalf("event = %+v", ev)
	}
	inc := ev.Closed
	if inc.Open() || !inc.End.Equal(t0.Add(40*time.Second)) || inc.Peak != model.SevCritical || inc.Ticks != 3 {
		t.Errorf("closed = %+v", inc)
	}
	if inc.Headline != "lan" || inc.Last != "lan again" || inc.Fix != "fix lan again" {
		t.Errorf("headlines = %q / %q / %q", inc.Headline, inc.Last, inc.Fix)
	}
	if inc.Duration(t0.Add(time.Hour)) != 40*time.Second {
		t.Errorf("duration = %v", inc.Duration(t0.Add(time.Hour)))
	}
	if g.Current() != nil {
		t.Error("incident still open after close")
	}
	// A further healthy tick is a no-op.
	if ev := g.Observe(t0.Add(90*time.Second), ok); ev.Opened != nil || ev.Closed != nil {
		t.Errorf("no-op event = %+v", ev)
	}
}

func TestCulpritChangeMerges(t *testing.T) {
	g := NewGrouper(0, 0)
	g.Observe(t0, bad(model.CulpritWiFi, model.SevDegraded, "wifi"))
	g.Observe(t0.Add(time.Second), bad(model.CulpritISPAccess, model.SevDegraded, "isp"))
	g.Observe(t0.Add(2*time.Second), bad(model.CulpritWiFi, model.SevDegraded, "wifi"))
	cur := g.Current()
	if cur.Culprit != model.CulpritWiFi {
		t.Errorf("culprit changed to %v", cur.Culprit)
	}
	if len(cur.Culprits) != 2 || cur.Culprits[0] != "Wi-Fi" || cur.Culprits[1] != "ISP access link" {
		t.Errorf("culprits = %v", cur.Culprits)
	}
	if _, n := g.Stats(); n != 1 {
		t.Errorf("incidents = %d", n)
	}
}

func TestListOrderExportImportClear(t *testing.T) {
	g := NewGrouper(10*time.Second, 0)
	// Two closed incidents, then one open.
	g.Observe(t0, bad(model.CulpritWiFi, model.SevDegraded, "a"))
	g.Observe(t0.Add(time.Minute), ok)
	g.Observe(t0.Add(2*time.Minute), bad(model.CulpritDNS, model.SevDegraded, "b"))
	g.Observe(t0.Add(2*time.Minute+time.Second), bad(model.CulpritDNS, model.SevDegraded, "b2"))
	g.Observe(t0.Add(3*time.Minute), ok)
	g.Observe(t0.Add(4*time.Minute), bad(model.CulpritUpstream, model.SevCritical, "c"))

	list := g.List()
	if len(list) != 3 || list[0].ID != 3 || !list[0].Open() || list[1].ID != 2 || list[2].ID != 1 {
		t.Fatalf("list = %+v", list)
	}

	exp, next := g.Export()
	if next != 4 || len(exp) != 3 {
		t.Fatalf("export = %d items, next %d", len(exp), next)
	}

	g2 := NewGrouper(10*time.Second, 0)
	g2.Import(exp, next)
	l2 := g2.List()
	if len(l2) != 3 || l2[0].ID != 3 || !l2[0].Open() || l2[1].ID != 2 || l2[2].ID != 1 {
		t.Fatalf("imported list = %+v", l2)
	}
	if raw, n := g2.Stats(); raw != 4 || n != 3 {
		t.Errorf("imported stats = %d/%d", raw, n)
	}
	// The reopened incident keeps counting and eventually closes.
	g2.Observe(t0.Add(5*time.Minute), bad(model.CulpritUpstream, model.SevDegraded, "c2"))
	if g2.Current().Ticks != 2 {
		t.Errorf("reopened ticks = %d", g2.Current().Ticks)
	}
	ev := g2.Observe(t0.Add(6*time.Minute), ok)
	if ev.Closed == nil || ev.Closed.ID != 3 {
		t.Errorf("close after import = %+v", ev)
	}
	// New incidents continue numbering from the exported nextID.
	ev = g2.Observe(t0.Add(7*time.Minute), bad(model.CulpritWiFi, model.SevDegraded, "d"))
	if ev.Opened == nil || ev.Opened.ID != 4 {
		t.Errorf("next id = %+v", ev.Opened)
	}

	// Import with nextID <= 0 derives it.
	g3 := NewGrouper(0, 0)
	g3.Import(exp, 0)
	if ev := g3.Observe(t0.Add(time.Hour), ok); ev.Closed == nil {
		t.Fatal("expected close")
	}
	if ev := g3.Observe(t0.Add(2*time.Hour), bad(model.CulpritWiFi, model.SevDegraded, "e")); ev.Opened == nil || ev.Opened.ID != 4 {
		t.Errorf("derived next id = %+v", ev.Opened)
	}

	g.Clear()
	if len(g.List()) != 0 || g.Current() != nil {
		t.Error("Clear left incidents")
	}
	if raw, n := g.Stats(); raw != 0 || n != 0 {
		t.Errorf("stats after clear = %d/%d", raw, n)
	}
	if ev := g.Observe(t0.Add(time.Hour), bad(model.CulpritWiFi, model.SevDegraded, "f")); ev.Opened == nil || ev.Opened.ID != 4 {
		t.Errorf("id after clear = %+v", ev.Opened)
	}
}

func TestKeepCap(t *testing.T) {
	g := NewGrouper(time.Second, 3)
	for i := 0; i < 6; i++ {
		base := t0.Add(time.Duration(i) * time.Minute)
		g.Observe(base, bad(model.CulpritWiFi, model.SevDegraded, "x"))
		g.Observe(base.Add(30*time.Second), ok)
	}
	list := g.List()
	if len(list) != 3 || list[0].ID != 6 || list[2].ID != 4 {
		t.Errorf("list = %+v", list)
	}
	if _, n := g.Stats(); n != 6 {
		t.Errorf("count = %d", n)
	}
}
