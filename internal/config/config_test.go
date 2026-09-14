package config

import (
	"encoding/json"
	"testing"

	"github.com/NYBaywatch/agent-smith/internal/synth"
)

func TestParseFillsDefaultsAndNormalizes(t *testing.T) {
	c, err := Parse([]byte(`{"synthetics":{"enabled":true,"interval_seconds":5,"custom":[{"url":"https://example.test/"}]},"slo":{"availability":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Synthetics.IntervalSeconds != 15 {
		t.Errorf("interval floor: got %d", c.Synthetics.IntervalSeconds)
	}
	if c.SLO.Availability != 0.999 {
		t.Errorf("bad SLO not reset: %v", c.SLO.Availability)
	}
	if got := c.Synthetics.Custom[0]; got.Category != synth.CategoryCustom || got.Name != "https://example.test/" {
		t.Errorf("custom check not normalized: %+v", got)
	}
	if c.Path.IntervalSeconds != 180 || c.Path.ProbesPerHop != 3 {
		t.Errorf("path defaults not applied: %+v", c.Path)
	}
}

func TestParseErrorReturnsDefaults(t *testing.T) {
	c, err := Parse([]byte(`{not json`))
	if err == nil {
		t.Fatal("expected error")
	}
	if c.SLO.Availability != Default().SLO.Availability {
		t.Error("defaults not returned on error")
	}
}

func TestChecksHonoursCategoriesDisabledAndCustom(t *testing.T) {
	c := Default()
	c.Synthetics.Categories = []string{"CDN"}
	cdn := synth.PresetsByCategory(synth.CategoryCDN)
	if len(cdn) == 0 {
		t.Skip("no CDN presets")
	}
	c.Synthetics.Disabled = []string{cdn[0].Name}
	off := false
	c.Synthetics.Custom = []synth.Check{
		{Name: "mine", URL: "https://internal.example/", Category: synth.CategoryCustom},
		{Name: "off", URL: "https://off.example/", Enabled: &off},
	}
	got := c.Checks()
	if len(got) != len(cdn) { // len(cdn)-1 presets + 1 enabled custom
		t.Fatalf("got %d checks, want %d", len(got), len(cdn))
	}
	for _, ck := range got {
		if ck.Name == cdn[0].Name || ck.Name == "off" {
			t.Errorf("disabled check present: %s", ck.Name)
		}
	}
	if got[len(got)-1].Name != "mine" {
		t.Errorf("custom check should be last: %+v", got[len(got)-1])
	}
	c.Synthetics.Enabled = false
	if len(c.Checks()) != 0 {
		t.Error("disabled synthetics should yield no checks")
	}
}

func TestDefaultRoundTrips(t *testing.T) {
	data, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if c.Synthetics.IntervalSeconds != 60 || !c.Notifications {
		t.Errorf("round trip changed values: %+v", c)
	}
}
