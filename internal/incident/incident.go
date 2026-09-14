// Package incident folds the classifier's per-tick verdicts into incidents so
// that a ten-minute outage is one event with a start, an end and a peak — not
// six hundred alerts. This is the single-host counterpart of alert-storm
// compression: the UI shows incidents (and the raw-tick-to-incident ratio)
// instead of every degraded sample.
package incident

import (
	"sort"
	"sync"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/model"
)

// Defaults used when NewGrouper is given zero values.
const (
	DefaultCooldown = 45 * time.Second
	DefaultKeep     = 100
)

// Incident is a contiguous run of degraded verdicts.
type Incident struct {
	ID       int            `json:"id"`
	Start    time.Time      `json:"start"`
	End      time.Time      `json:"end"` // zero while open
	Culprit  model.Culprit  `json:"culprit"`
	Peak     model.Severity `json:"peak"`
	Ticks    int            `json:"ticks"`    // degraded ticks folded into this incident
	Headline string         `json:"headline"` // first headline
	Last     string         `json:"last"`     // latest headline
	Fix      string         `json:"fix"`
	// Culprits lists the distinct culprit names seen, in order. A Wi-Fi
	// incident that turns into an ISP one stays a single incident for as long
	// as the connection is continuously unhealthy.
	Culprits []string `json:"culprits"`
}

// Open reports whether the incident is still in progress.
func (i Incident) Open() bool { return i.End.IsZero() }

// Duration is how long the incident lasted (up to now while open).
func (i Incident) Duration(now time.Time) time.Duration {
	if i.Open() {
		return now.Sub(i.Start)
	}
	return i.End.Sub(i.Start)
}

// clone returns a copy with its own Culprits slice.
func (i Incident) clone() Incident {
	i.Culprits = append([]string(nil), i.Culprits...)
	return i
}

// Event describes what a call to Observe changed. Both fields are nil when
// nothing opened or closed.
type Event struct {
	Opened *Incident
	Closed *Incident
}

// Grouper turns verdicts into incidents. It is safe for concurrent use.
type Grouper struct {
	mu       sync.Mutex
	cooldown time.Duration
	keep     int
	nextID   int
	open     *Incident
	lastBad  time.Time  // last degraded tick folded into the open incident
	closed   []Incident // oldest first
	rawTicks int        // degraded ticks observed overall
	count    int        // incidents opened overall
}

// NewGrouper returns a Grouper. cooldown is how long verdicts must stay healthy
// (Severity < SevDegraded) before an open incident closes; keep caps the number
// of closed incidents retained. Zero values pick the defaults.
func NewGrouper(cooldown time.Duration, keep int) *Grouper {
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	if keep <= 0 {
		keep = DefaultKeep
	}
	return &Grouper{cooldown: cooldown, keep: keep, nextID: 1}
}

// Observe folds one verdict in. A degraded tick opens an incident immediately
// (a one-tick flap is still recorded — its Ticks and Duration make its size
// obvious) or extends the open one; a healthy tick closes the open incident
// once the cooldown has elapsed since the last degraded tick.
func (g *Grouper) Observe(now time.Time, v model.Verdict) Event {
	g.mu.Lock()
	defer g.mu.Unlock()

	if v.Severity >= model.SevDegraded {
		g.rawTicks++
		g.lastBad = now
		if g.open == nil {
			g.count++
			g.open = &Incident{
				ID: g.nextID, Start: now, Culprit: v.Culprit, Peak: v.Severity, Ticks: 1,
				Headline: v.Headline, Last: v.Headline, Fix: v.Fix, Culprits: []string{v.Culprit.String()},
			}
			g.nextID++
			cp := g.open.clone()
			return Event{Opened: &cp}
		}
		inc := g.open
		inc.Ticks++
		if v.Severity > inc.Peak {
			inc.Peak = v.Severity
		}
		inc.Last = v.Headline
		if v.Fix != "" {
			inc.Fix = v.Fix
		}
		if name := v.Culprit.String(); !contains(inc.Culprits, name) {
			inc.Culprits = append(inc.Culprits, name)
		}
		return Event{}
	}

	if g.open == nil || now.Sub(g.lastBad) < g.cooldown {
		return Event{}
	}
	inc := *g.open
	inc.End = g.lastBad
	g.open = nil
	g.closed = append(g.closed, inc)
	g.trim()
	cp := inc.clone()
	return Event{Closed: &cp}
}

// trim drops the oldest closed incidents beyond keep.
func (g *Grouper) trim() {
	if len(g.closed) > g.keep {
		g.closed = append([]Incident(nil), g.closed[len(g.closed)-g.keep:]...)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Current returns a copy of the open incident, or nil.
func (g *Grouper) Current() *Incident {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.open == nil {
		return nil
	}
	cp := g.open.clone()
	return &cp
}

// List returns all incidents, newest first, with the open one (if any) at the
// head.
func (g *Grouper) List() []Incident {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.list()
}

func (g *Grouper) list() []Incident {
	out := make([]Incident, 0, len(g.closed)+1)
	if g.open != nil {
		out = append(out, g.open.clone())
	}
	for i := len(g.closed) - 1; i >= 0; i-- {
		out = append(out, g.closed[i].clone())
	}
	return out
}

// Export returns the incidents (newest first, as List) and the next ID to
// assign, for persistence.
func (g *Grouper) Export() (list []Incident, nextID int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.list(), g.nextID
}

// Import restores incidents previously returned by Export (any order; an open
// incident, if present, is reopened). nextID <= 0 derives the next ID from the
// highest one seen. The raw-tick and incident counters are rebuilt from the
// imported incidents.
func (g *Grouper) Import(list []Incident, nextID int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open = nil
	g.closed = nil
	g.rawTicks, g.count = 0, 0
	maxID := 0
	for _, inc := range list {
		inc = inc.clone()
		if inc.ID > maxID {
			maxID = inc.ID
		}
		g.rawTicks += inc.Ticks
		g.count++
		if inc.Open() && g.open == nil {
			cp := inc
			g.open = &cp
			g.lastBad = inc.Start
			continue
		}
		if inc.Open() {
			inc.End = inc.Start // a second "open" incident can't be: close it
		}
		g.closed = append(g.closed, inc)
	}
	sort.SliceStable(g.closed, func(i, j int) bool { return g.closed[i].Start.Before(g.closed[j].Start) })
	g.trim()
	if nextID <= 0 {
		nextID = maxID + 1
	}
	g.nextID = nextID
}

// Stats returns the total degraded ticks observed and the number of incidents
// they were compressed into.
func (g *Grouper) Stats() (raw, incidents int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.rawTicks, g.count
}

// Clear forgets all incidents (open and closed) and resets the counters; ID
// numbering continues from where it was.
func (g *Grouper) Clear() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open = nil
	g.closed = nil
	g.rawTicks = 0
	g.count = 0
}
