package pathmon

import (
	"sync"
	"time"
)

// RouteChange records that the path to a destination differs from the
// previous trace — the single-host analogue of a BGP route change.
type RouteChange struct {
	When       time.Time `json:"when"`
	Name       string    `json:"name"`
	Dest       string    `json:"dest"`
	Before     string    `json:"before"`    // previous hop signature
	After      string    `json:"after"`     // new hop signature
	ASBefore   string    `json:"as_before"` // previous AS signature
	ASAfter    string    `json:"as_after"`  // new AS signature
	HopsBefore int       `json:"hops_before"`
	HopsAfter  int       `json:"hops_after"`
}

const (
	defaultKeep = 10
	changeCap   = 50
)

// Monitor retains recent paths per destination and flags route changes. It is
// safe for concurrent use.
type Monitor struct {
	mu      sync.Mutex
	keep    int
	order   []string          // destinations in first-seen order
	paths   map[string][]Path // dest → oldest…newest
	changes []RouteChange     // newest first
}

// NewMonitor returns a Monitor keeping up to keep paths per destination
// (default 10 when keep <= 0).
func NewMonitor(keep int) *Monitor {
	if keep <= 0 {
		keep = defaultKeep
	}
	return &Monitor{keep: keep, paths: make(map[string][]Path)}
}

// Add records a path and returns a RouteChange when its route differs from the
// previous path to the same destination. Silent hops ("*") are ignored when
// comparing — a router that flaps between answering and not answering has not
// moved — so only positions where both traces have an address are compared, in
// addition to a hop-count change of two or more.
func (m *Monitor) Add(p Path) *RouteChange {
	m.mu.Lock()
	defer m.mu.Unlock()

	hist, seen := m.paths[p.Dest]
	if !seen {
		m.order = append(m.order, p.Dest)
	}
	var change *RouteChange
	if len(hist) > 0 {
		prev := hist[len(hist)-1]
		if routeDiffers(prev, p) {
			change = &RouteChange{
				When: p.When, Name: p.Name, Dest: p.Dest,
				Before: prev.Signature(), After: p.Signature(),
				ASBefore: prev.ASSignature(), ASAfter: p.ASSignature(),
				HopsBefore: len(prev.Hops), HopsAfter: len(p.Hops),
			}
			m.changes = append([]RouteChange{*change}, m.changes...)
			if len(m.changes) > changeCap {
				m.changes = m.changes[:changeCap]
			}
		}
	}
	hist = append(hist, p)
	if len(hist) > m.keep {
		hist = hist[len(hist)-m.keep:]
	}
	m.paths[p.Dest] = hist
	return change
}

// routeDiffers implements the comparison rule described on Add.
func routeDiffers(a, b Path) bool {
	if abs(len(a.Hops)-len(b.Hops)) >= 2 {
		return true
	}
	n := len(a.Hops)
	if len(b.Hops) < n {
		n = len(b.Hops)
	}
	for i := 0; i < n; i++ {
		ha, hb := a.Hops[i], b.Hops[i]
		if ha.Addr == "" || hb.Addr == "" {
			continue
		}
		if ha.Addr != hb.Addr {
			return true
		}
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Latest returns the newest path per destination, in first-seen order.
func (m *Monitor) Latest() []Path {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Path, 0, len(m.order))
	for _, d := range m.order {
		if h := m.paths[d]; len(h) > 0 {
			out = append(out, h[len(h)-1])
		}
	}
	return out
}

// Changes returns recorded route changes, most recent first (at most 50).
func (m *Monitor) Changes() []RouteChange {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RouteChange, len(m.changes))
	copy(out, m.changes)
	return out
}

// History returns the retained paths for dest, oldest first.
func (m *Monitor) History(dest string) []Path {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.paths[dest]
	out := make([]Path, len(h))
	copy(out, h)
	return out
}
