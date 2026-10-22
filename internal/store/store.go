// Package store persists Agent Smith's session state — RTT history and recorded
// issues — to a JSON file under the user's config directory, so history and the
// issue log survive restarts.
package store

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/NYBaywatch/agent-smith/internal/baseline"
	"github.com/NYBaywatch/agent-smith/internal/bufferbloat"
	"github.com/NYBaywatch/agent-smith/internal/dnsbench"
	"github.com/NYBaywatch/agent-smith/internal/incident"
	"github.com/NYBaywatch/agent-smith/internal/model"
	"github.com/NYBaywatch/agent-smith/internal/pathmon"
	"github.com/NYBaywatch/agent-smith/internal/speedtest"
	"github.com/NYBaywatch/agent-smith/internal/stability"
)

// Tests holds the last result of each on-demand test.
type Tests struct {
	Bufferbloat *bufferbloat.Result `json:"bufferbloat,omitempty"`
	Speed       *speedtest.Result   `json:"speed,omitempty"`
	Stability   *stability.Result   `json:"stability,omitempty"`
	DNSBench    *dnsbench.Result    `json:"dnsbench,omitempty"`
}

// State is the persisted document. Version 2 adds the long-term baseline
// series, grouped incidents and recent route changes; a version-1 file loads
// with those fields empty.
type State struct {
	Version int               `json:"version"`
	History []model.HistPoint `json:"history"`
	Issues  []model.Issue     `json:"issues"`

	Baseline       []baseline.Series     `json:"baseline,omitempty"`
	Incidents      []incident.Incident   `json:"incidents,omitempty"`
	NextIncidentID int                   `json:"next_incident_id,omitempty"`
	RouteChanges   []pathmon.RouteChange `json:"route_changes,omitempty"`
	Tests          Tests                 `json:"tests,omitempty"`
}

const currentVersion = 2

// Dir returns the Agent Smith data directory, creating it if necessary.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "AgentSmith")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// Path returns the full path to the state file.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// Load reads persisted state. A missing file yields an empty State and no error.
func Load() (State, error) {
	var s State
	p, err := Path()
	if err != nil {
		return s, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, err
	}
	return s, nil
}

// Save atomically writes state to disk (temp file + rename).
func Save(s State) error {
	s.Version = currentVersion
	p, err := Path()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
