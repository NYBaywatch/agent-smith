// Package config loads Agent Smith's optional user configuration file
// (config.json in the AgentSmith data directory). It lets users add their own
// ping anchors (game servers, a VPN endpoint), their own synthetic HTTP checks
// (an internal app, a partner API), choose which built-in synthetic categories
// run, and set the SLO that SLA compliance is judged against. A missing file
// yields defaults and a template is written so users can discover the knobs; a
// malformed file is reported but never fatal.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/store"
	"github.com/NYBaywatch/agent-smith/internal/synth"
)

// Anchor is a user-added ICMP target (public internet ring).
type Anchor struct {
	Name string `json:"name"`
	Host string `json:"host"` // IP address (names are resolved once at startup)
}

// Synthetics configures the HTTP synthetic monitoring loop.
type Synthetics struct {
	Enabled         bool          `json:"enabled"`
	IntervalSeconds int           `json:"interval_seconds"` // cadence for the whole check set
	Categories      []string      `json:"categories"`       // built-in preset categories to run: SaaS, Cloud, CDN, AI API
	Custom          []synth.Check `json:"custom"`           // user-defined checks
	Disabled        []string      `json:"disabled"`         // preset URLs or names to skip
}

// Path configures hop-by-hop route monitoring.
type Path struct {
	Enabled         bool `json:"enabled"`
	IntervalSeconds int  `json:"interval_seconds"`
	ProbesPerHop    int  `json:"probes_per_hop"`
}

// SLO is the service-level objective SLA compliance is measured against.
type SLO struct {
	Availability float64 `json:"availability"` // e.g. 0.999
	P95Ms        float64 `json:"p95_ms"`       // internet RTT objective; 0 disables
}

// Config is the on-disk document.
type Config struct {
	Version       int        `json:"version"`
	Comment       string     `json:"_comment,omitempty"`
	Anchors       []Anchor   `json:"anchors"`
	Synthetics    Synthetics `json:"synthetics"`
	Path          Path       `json:"path"`
	SLO           SLO        `json:"slo"`
	Notifications bool       `json:"notifications"` // tray balloon when an incident opens/closes
}

const currentVersion = 1

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Version: currentVersion,
		Comment: "Agent Smith user configuration. Add your own anchors (IPs) and synthetic HTTP checks; " +
			"categories: SaaS, Cloud, CDN, AI API. Restart the app after editing.",
		Anchors: nil,
		Synthetics: Synthetics{
			Enabled:         true,
			IntervalSeconds: 60,
			Categories:      []string{"SaaS", "Cloud", "CDN", "AI API"},
			Custom:          nil,
			Disabled:        nil,
		},
		Path:          Path{Enabled: true, IntervalSeconds: 180, ProbesPerHop: 3},
		SLO:           SLO{Availability: 0.999, P95Ms: 100},
		Notifications: true,
	}
}

// FilePath returns the config file location.
func FilePath() (string, error) {
	dir, err := store.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config file. When the file does not exist, the defaults are
// written to disk (best effort) and returned. A parse error returns the
// defaults together with the error so the caller can surface it.
func Load() (Config, error) {
	def := Default()
	p, err := FilePath()
	if err != nil {
		return def, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_ = Save(def)
			return def, nil
		}
		return def, err
	}
	return Parse(data)
}

// Parse decodes a config document, filling unset fields with defaults.
func Parse(data []byte) (Config, error) {
	c := Default()
	if err := json.Unmarshal(data, &c); err != nil {
		return Default(), err
	}
	c.normalize()
	return c, nil
}

// Save writes the config atomically.
func Save(c Config) error {
	c.Version = currentVersion
	p, err := FilePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (c *Config) normalize() {
	if c.Synthetics.IntervalSeconds <= 0 {
		c.Synthetics.IntervalSeconds = 60
	}
	if c.Synthetics.IntervalSeconds < 15 {
		c.Synthetics.IntervalSeconds = 15
	}
	if c.Path.IntervalSeconds <= 0 {
		c.Path.IntervalSeconds = 180
	}
	if c.Path.IntervalSeconds < 30 {
		c.Path.IntervalSeconds = 30
	}
	if c.Path.ProbesPerHop <= 0 {
		c.Path.ProbesPerHop = 3
	}
	if c.SLO.Availability <= 0 || c.SLO.Availability > 1 {
		c.SLO.Availability = 0.999
	}
	if c.SLO.P95Ms < 0 {
		c.SLO.P95Ms = 0
	}
	for i := range c.Synthetics.Custom {
		if c.Synthetics.Custom[i].Category == "" {
			c.Synthetics.Custom[i].Category = synth.CategoryCustom
		}
		if c.Synthetics.Custom[i].Name == "" {
			c.Synthetics.Custom[i].Name = c.Synthetics.Custom[i].URL
		}
	}
}

// SyntheticInterval returns the synthetic cadence as a Duration.
func (c Config) SyntheticInterval() time.Duration {
	return time.Duration(c.Synthetics.IntervalSeconds) * time.Second
}

// PathInterval returns the route-monitoring cadence as a Duration.
func (c Config) PathInterval() time.Duration {
	return time.Duration(c.Path.IntervalSeconds) * time.Second
}

// Checks assembles the effective synthetic check set: presets from the enabled
// categories (minus any disabled by URL or name) followed by custom checks.
func (c Config) Checks() []synth.Check {
	if !c.Synthetics.Enabled {
		return nil
	}
	disabled := map[string]bool{}
	for _, d := range c.Synthetics.Disabled {
		disabled[d] = true
	}
	var out []synth.Check
	for _, cat := range c.Synthetics.Categories {
		for _, ck := range synth.PresetsByCategory(synth.Category(cat)) {
			if disabled[ck.URL] || disabled[ck.Name] {
				continue
			}
			out = append(out, ck)
		}
	}
	for _, ck := range c.Synthetics.Custom {
		if ck.URL == "" || !ck.IsEnabled() {
			continue
		}
		out = append(out, ck)
	}
	return out
}
