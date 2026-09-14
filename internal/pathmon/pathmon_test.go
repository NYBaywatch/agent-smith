package pathmon

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/probe"
)

// hopScript describes how the fake network answers probes at one TTL.
type hopScript struct {
	addr     string        // "" = silent
	rtt      time.Duration // reply RTT
	dropMod  int           // drop every dropMod-th probe (0 = never drop)
	dest     bool          // this TTL is the destination (echo reply)
	rttSpike time.Duration // added to the first probe only
}

// fakePinger implements probe.Pinger from a per-TTL script.
type fakePinger struct {
	mu     sync.Mutex
	script map[int]hopScript
	calls  map[int]int
}

func newFake(script map[int]hopScript) *fakePinger {
	return &fakePinger{script: script, calls: make(map[int]int)}
}

func (f *fakePinger) Ping(context.Context, net.IP, time.Duration) (probe.Result, error) {
	return probe.Result{}, nil
}

func (f *fakePinger) PingTTL(_ context.Context, _ net.IP, ttl int, _ time.Duration) (probe.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[ttl]++
	n := f.calls[ttl]
	s, ok := f.script[ttl]
	if !ok || s.addr == "" {
		return probe.Result{Status: "timeout"}, nil
	}
	if s.dropMod > 0 && n%s.dropMod == 0 {
		return probe.Result{Status: "timeout"}, nil
	}
	rtt := s.rtt
	if n == 1 {
		rtt += s.rttSpike
	}
	r := probe.Result{Addr: net.ParseIP(s.addr), RTT: rtt, Status: "ok"}
	if s.dest {
		r.OK = true
	} else {
		r.TTLExpired = true
		r.Status = "ttl-expired"
	}
	return r, nil
}

func (f *fakePinger) Close() error { return nil }

func (f *fakePinger) maxTTL() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := 0
	for t := range f.calls {
		if t > m {
			m = t
		}
	}
	return m
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestTraceReachesDestination(t *testing.T) {
	fp := newFake(map[int]hopScript{
		1: {addr: "192.168.1.1", rtt: ms(1)},
		2: {addr: "67.83.0.1", rtt: ms(9)},
		3: {addr: "", rtt: 0},                          // silent transit hop
		4: {addr: "4.68.0.1", rtt: ms(12), dropMod: 3}, // one of three dropped
		5: {addr: "1.1.1.1", rtt: ms(14), dest: true},
		6: {addr: "9.9.9.9", rtt: ms(99)}, // must never appear
	})
	opt := Options{ProbesPerHop: 3, PerHop: 50 * time.Millisecond}
	p, err := Trace(context.Background(), fp, "Cloudflare", net.ParseIP("1.1.1.1"), opt)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Reached {
		t.Error("expected Reached")
	}
	if len(p.Hops) != 5 {
		t.Fatalf("expected 5 hops, got %d: %s", len(p.Hops), p.Signature())
	}
	if p.Dest != "1.1.1.1" || p.Name != "Cloudflare" {
		t.Errorf("dest/name: %q %q", p.Dest, p.Name)
	}
	if h := p.Hops[0]; h.Addr != "192.168.1.1" || !h.Private || h.Recv != 3 || h.Loss != 0 || h.Avg != ms(1) {
		t.Errorf("hop1: %+v", h)
	}
	if h := p.Hops[1]; h.Private {
		t.Errorf("hop2 should be public: %+v", h)
	}
	if h := p.Hops[2]; h.Responded() || h.Sent != 3 || h.Loss != 1 {
		t.Errorf("hop3 should be silent with 100%% loss: %+v", h)
	}
	if h := p.Hops[3]; h.Recv != 2 || h.Sent != 3 || h.Loss < 0.33 || h.Loss > 0.34 {
		t.Errorf("hop4 loss: %+v", h)
	}
	if h := p.Hops[4]; h.Addr != "1.1.1.1" || h.Min != ms(14) || h.Max != ms(14) {
		t.Errorf("hop5: %+v", h)
	}
	if got := p.Signature(); got != "192.168.1.1>67.83.0.1>*>4.68.0.1>1.1.1.1" {
		t.Errorf("signature %q", got)
	}
	if p.Elapsed <= 0 {
		t.Error("expected Elapsed > 0")
	}
}

func TestTraceStopsAfterSilentRun(t *testing.T) {
	fp := newFake(map[int]hopScript{
		1: {addr: "192.168.1.1", rtt: ms(1)},
		2: {addr: "67.83.0.1", rtt: ms(9)},
		// everything else silent
	})
	opt := Options{ProbesPerHop: 1, PerHop: 20 * time.Millisecond, MaxHops: 30}
	p, err := Trace(context.Background(), fp, "x", net.ParseIP("203.0.113.9"), opt)
	if err != nil {
		t.Fatal(err)
	}
	if p.Reached {
		t.Error("should not be reached")
	}
	if len(p.Hops) != 2 {
		t.Errorf("trailing silent hops should be trimmed, got %d (%s)", len(p.Hops), p.Signature())
	}
	if m := fp.maxTTL(); m >= 20 {
		t.Errorf("should have given up early, probed up to TTL %d", m)
	}
}

func TestTraceRespectsContext(t *testing.T) {
	fp := newFake(map[int]hopScript{1: {addr: "192.168.1.1", rtt: ms(1)}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Trace(ctx, fp, "x", net.ParseIP("1.1.1.1"), Options{ProbesPerHop: 1})
	if err == nil {
		t.Error("expected context error")
	}
}

func TestComputeASPathAndASSignature(t *testing.T) {
	hops := []Hop{{ASN: 0}, {ASN: 6128}, {ASN: 6128}, {ASN: 0}, {ASN: 3356}, {ASN: 13335}, {ASN: 13335}}
	got := ComputeASPath(hops)
	want := []int{6128, 3356, 13335}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	p := Path{ASPath: got}
	if s := p.ASSignature(); s != "AS6128>AS3356>AS13335" {
		t.Errorf("ASSignature %q", s)
	}
	if s := (Path{}).ASSignature(); s != "" {
		t.Errorf("empty ASSignature %q", s)
	}
}

// sample builds a reached 6-hop path: LAN, ISP (2 hops, AS6128), transit, dest.
func sample(loss []float64, avg []int) Path {
	p := Path{Name: "t", Dest: "1.1.1.1", Reached: true}
	addrs := []string{"192.168.1.1", "67.83.0.1", "67.83.0.9", "4.68.0.1", "172.71.0.1", "1.1.1.1"}
	asns := []int{0, 6128, 6128, 3356, 13335, 13335}
	for i := range addrs {
		h := Hop{TTL: i + 1, Addr: addrs[i], ASN: asns[i], Sent: 10, Private: i == 0}
		h.Loss = loss[i]
		h.Recv = 10 - int(loss[i]*10)
		h.Avg = ms(avg[i])
		p.Hops = append(p.Hops, h)
	}
	return p
}

func TestSegments(t *testing.T) {
	p := sample([]float64{0, 0, 0, 0, 0, 0}, []int{1, 9, 10, 12, 13, 14})
	// Insert a silent hop after the ISP hops to check inheritance.
	p.Hops = append(p.Hops[:3], append([]Hop{{TTL: 99}}, p.Hops[3:]...)...)
	got := Segments(p)
	want := []Segment{SegmentLAN, SegmentISP, SegmentISP, SegmentISP, SegmentTransit, SegmentTransit, SegmentDestination}
	if len(got) != len(want) {
		t.Fatalf("len %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("hop %d: got %s want %s", i, got[i], want[i])
		}
	}
	// Silent first hop on an unreached path is LAN.
	q := Path{Hops: []Hop{{TTL: 1}, {TTL: 2, Addr: "67.83.0.1"}}}
	if s := Segments(q); s[0] != SegmentLAN || s[1] != SegmentISP {
		t.Errorf("unreached: %v", s)
	}
	// An ISP edge router with no ASN mapping, followed by hops in the ISP's
	// AS, is still part of the ISP segment; the ISP span ends at the last hop
	// carrying that ASN even when an unmapped hop sits inside it.
	r := Path{Reached: true, Hops: []Hop{
		{TTL: 1, Addr: "192.168.1.1", Private: true},
		{TTL: 2, Addr: "67.59.233.44"},               // edge, ASN unknown
		{TTL: 3, Addr: "67.83.250.128", ASN: 6128},   // ISP core
		{TTL: 4, Addr: "63.142.22.160"},              // unmapped, inside ISP span
		{TTL: 5, Addr: "65.19.113.156", ASN: 6128},   // ISP core
		{TTL: 6, Addr: "173.245.63.162", ASN: 13335}, // transit / peer
		{TTL: 7, Addr: "1.1.1.1", ASN: 13335},
	}}
	want2 := []Segment{SegmentLAN, SegmentISP, SegmentISP, SegmentISP, SegmentISP, SegmentTransit, SegmentDestination}
	for i, s := range Segments(r) {
		if s != want2[i] {
			t.Errorf("edge-unmapped hop %d: got %s want %s", i, s, want2[i])
		}
	}
}

func TestFirstDegradedHop(t *testing.T) {
	const lossThr, jumpThr = 0.05, 40 * time.Millisecond

	clean := sample([]float64{0, 0, 0, 0, 0, 0}, []int{1, 9, 10, 12, 13, 14})
	if d := FirstDegradedHop(clean, lossThr, ms(40)); d.HopIndex != -1 {
		t.Errorf("clean path flagged: %+v", d)
	}

	// Loss confined to a middle hop = ICMP rate limiting, not a problem.
	rateLimited := sample([]float64{0, 0, 0.6, 0, 0, 0}, []int{1, 9, 10, 12, 13, 14})
	if d := FirstDegradedHop(rateLimited, lossThr, jumpThr); d.HopIndex != -1 {
		t.Errorf("rate-limited hop flagged: %+v", d)
	}

	// Real loss from hop 4 onwards.
	realLoss := sample([]float64{0, 0, 0, 0.2, 0.3, 0.2}, []int{1, 9, 10, 12, 13, 14})
	d := FirstDegradedHop(realLoss, lossThr, jumpThr)
	if d.HopIndex != 3 || d.Segment != SegmentTransit {
		t.Errorf("real loss: %+v", d)
	}
	if d.Reason == "" {
		t.Error("expected a reason")
	}

	// Latency spike at one hop only = de-prioritised ICMP, ignore.
	spike := sample([]float64{0, 0, 0, 0, 0, 0}, []int{1, 9, 250, 12, 13, 14})
	if d := FirstDegradedHop(spike, lossThr, jumpThr); d.HopIndex != -1 {
		t.Errorf("isolated spike flagged: %+v", d)
	}

	// Persistent jump introduced at hop 3 (ISP).
	jump := sample([]float64{0, 0, 0, 0, 0, 0}, []int{1, 9, 95, 97, 99, 100})
	d = FirstDegradedHop(jump, lossThr, jumpThr)
	if d.HopIndex != 2 || d.Segment != SegmentISP {
		t.Errorf("persistent jump: %+v", d)
	}

	// Loss wins over latency when both present.
	both := sample([]float64{0, 0, 0, 0, 0.5, 0.5}, []int{1, 9, 95, 97, 99, 100})
	if d := FirstDegradedHop(both, lossThr, jumpThr); d.HopIndex != 4 {
		t.Errorf("loss precedence: %+v", d)
	}

	// Lossy destination only: real (nothing downstream to contradict it).
	destLoss := sample([]float64{0, 0, 0, 0, 0, 0.3}, []int{1, 9, 10, 12, 13, 14})
	if d := FirstDegradedHop(destLoss, lossThr, jumpThr); d.HopIndex != 5 || d.Segment != SegmentDestination {
		t.Errorf("destination loss: %+v", d)
	}

	if d := FirstDegradedHop(Path{}, lossThr, jumpThr); d.HopIndex != -1 {
		t.Errorf("empty path: %+v", d)
	}
}

func pathOf(dest string, when time.Time, addrs ...string) Path {
	p := Path{Name: dest, Dest: dest, When: when}
	for i, a := range addrs {
		p.Hops = append(p.Hops, Hop{TTL: i + 1, Addr: a})
	}
	return p
}

func TestMonitorRouteChanges(t *testing.T) {
	m := NewMonitor(3)
	t0 := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	if c := m.Add(pathOf("1.1.1.1", t0, "192.168.1.1", "67.83.0.1", "4.68.0.1", "1.1.1.1")); c != nil {
		t.Errorf("first path should not be a change: %+v", c)
	}
	// Same route, one hop silent this time: not a change.
	if c := m.Add(pathOf("1.1.1.1", t0.Add(time.Minute), "192.168.1.1", "", "4.68.0.1", "1.1.1.1")); c != nil {
		t.Errorf("silent hop flagged as change: %+v", c)
	}
	// Silent hop comes back with the same address: still not a change.
	if c := m.Add(pathOf("1.1.1.1", t0.Add(2*time.Minute), "192.168.1.1", "67.83.0.1", "4.68.0.1", "1.1.1.1")); c != nil {
		t.Errorf("returning hop flagged as change: %+v", c)
	}
	// Different transit hop: a change.
	c := m.Add(pathOf("1.1.1.1", t0.Add(3*time.Minute), "192.168.1.1", "67.83.0.1", "4.69.0.7", "1.1.1.1"))
	if c == nil {
		t.Fatal("expected route change")
	}
	if c.Dest != "1.1.1.1" || c.HopsBefore != 4 || c.HopsAfter != 4 ||
		c.Before != "192.168.1.1>67.83.0.1>4.68.0.1>1.1.1.1" || c.After != "192.168.1.1>67.83.0.1>4.69.0.7>1.1.1.1" {
		t.Errorf("change: %+v", c)
	}
	// Hop count differs by one (extra silent hop): not a change.
	if c := m.Add(pathOf("1.1.1.1", t0.Add(4*time.Minute), "192.168.1.1", "67.83.0.1", "4.69.0.7", "", "1.1.1.1")); c != nil {
		t.Errorf("+1 hop flagged: %+v", c)
	}
	// Hop count differs by two: a change even if the compared prefix matches.
	if c := m.Add(pathOf("1.1.1.1", t0.Add(5*time.Minute), "192.168.1.1", "67.83.0.1", "4.69.0.7", "", "", "", "1.1.1.1")); c == nil {
		t.Error("+2 hops should be a change")
	}

	// Second destination, independent history.
	m.Add(pathOf("8.8.8.8", t0, "192.168.1.1", "67.83.0.1", "8.8.8.8"))

	latest := m.Latest()
	if len(latest) != 2 || latest[0].Dest != "1.1.1.1" || latest[1].Dest != "8.8.8.8" {
		t.Errorf("latest: %+v", latest)
	}
	if len(latest[0].Hops) != 7 {
		t.Errorf("latest should be the newest path, got %d hops", len(latest[0].Hops))
	}
	if h := m.History("1.1.1.1"); len(h) != 3 {
		t.Errorf("history should be capped at keep=3, got %d", len(h))
	}
	ch := m.Changes()
	if len(ch) != 2 || !ch[0].When.After(ch[1].When) {
		t.Errorf("changes should be newest first: %+v", ch)
	}
	if h := m.History("nope"); len(h) != 0 {
		t.Errorf("unknown dest history: %v", h)
	}
}

func TestMonitorChangeCap(t *testing.T) {
	m := NewMonitor(0)
	for i := 0; i < changeCap+10; i++ {
		addr := "10.0.0.1"
		if i%2 == 1 {
			addr = "10.0.0.2"
		}
		m.Add(pathOf("d", time.Unix(int64(i), 0), addr, "1.1.1.1"))
	}
	if n := len(m.Changes()); n != changeCap {
		t.Errorf("expected %d changes, got %d", changeCap, n)
	}
	if n := len(m.History("d")); n != defaultKeep {
		t.Errorf("expected default keep %d, got %d", defaultKeep, n)
	}
}
