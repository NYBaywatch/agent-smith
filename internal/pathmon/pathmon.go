// Package pathmon provides hop-by-hop path monitoring: a multi-probe traceroute
// with per-hop RTT/loss statistics, reverse-DNS and ASN enrichment, route-change
// detection, and "where does the degradation start?" analysis. It is Agent
// Smith's single-vantage-point counterpart to the path visualisation and
// topology-aware probable-cause features of commercial internet performance
// monitors.
//
// Reading a traceroute correctly matters: routers routinely rate-limit or
// de-prioritise the ICMP replies they generate for TTL-expired probes, so loss
// or a latency spike at a middle hop that does *not* carry through to later
// hops is an artefact, not a problem. FirstDegradedHop encodes that rule.
package pathmon

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/asn"
	"github.com/NYBaywatch/agent-smith/internal/probe"
)

// Hop is one router along the path with statistics across the probes sent to it.
type Hop struct {
	TTL     int           `json:"ttl"`
	Addr    string        `json:"addr"`    // "" when the hop never answered ("*")
	Host    string        `json:"host"`    // reverse DNS, best effort, "" if none
	ASN     int           `json:"asn"`     // 0 when unknown / private
	ASName  string        `json:"as_name"` // short AS name, "" if unknown
	Sent    int           `json:"sent"`
	Recv    int           `json:"recv"`
	Loss    float64       `json:"loss"` // 0..1
	Min     time.Duration `json:"min"`
	Avg     time.Duration `json:"avg"`
	Max     time.Duration `json:"max"`
	Private bool          `json:"private"` // non-public address (LAN / CGNAT / reserved)
}

// Responded reports whether at least one probe to this hop got a reply.
func (h Hop) Responded() bool { return h.Addr != "" }

// Path is the result of one traceroute run to a destination.
type Path struct {
	Name    string        `json:"name"` // display name of the destination (e.g. "Cloudflare")
	Dest    string        `json:"dest"` // destination IP
	When    time.Time     `json:"when"`
	Hops    []Hop         `json:"hops"`
	Reached bool          `json:"reached"`
	ASPath  []int         `json:"as_path"` // consecutive-unique public ASNs in path order
	Elapsed time.Duration `json:"elapsed"`
}

// Signature is the hop-address sequence ("*" for silent hops) joined by ">".
// Two runs with the same signature took the same route.
func (p Path) Signature() string {
	parts := make([]string, len(p.Hops))
	for i, h := range p.Hops {
		if h.Addr == "" {
			parts[i] = "*"
		} else {
			parts[i] = h.Addr
		}
	}
	return strings.Join(parts, ">")
}

// ASSignature is the AS-level route, e.g. "AS6128>AS3356>AS13335".
func (p Path) ASSignature() string {
	parts := make([]string, len(p.ASPath))
	for i, a := range p.ASPath {
		parts[i] = fmt.Sprintf("AS%d", a)
	}
	return strings.Join(parts, ">")
}

// Options tunes a Trace run.
type Options struct {
	MaxHops      int           // TTL ceiling (default 30)
	ProbesPerHop int           // probes sent to each TTL (default 3)
	PerHop       time.Duration // per-probe timeout (default 1s)
	Enrich       bool          // resolve reverse DNS and ASN for each responding hop
	RDNSTimeout  time.Duration // per-hop reverse lookup bound (default 500ms)
	ASN          *asn.Resolver // nil disables ASN enrichment
}

// DefaultOptions returns sensible defaults (enrichment on, no ASN resolver).
func DefaultOptions() Options {
	return Options{
		MaxHops:      30,
		ProbesPerHop: 3,
		PerHop:       time.Second,
		Enrich:       true,
		RDNSTimeout:  500 * time.Millisecond,
	}
}

const (
	batchTTLs       = 4 // TTLs probed concurrently
	silentStop      = 5 // consecutive silent hops after which we give up
	enrichParallel  = 6
	jumpPersistFuzz = 5 * time.Millisecond
)

// Trace runs a multi-probe traceroute from this host to dest. TTLs are probed
// in small concurrent batches; probing stops once the destination replies or
// after silentStop consecutive unanswered hops. Hops beyond the destination
// are discarded. When opt.Enrich is set, responding hops are annotated with
// reverse DNS and (if opt.ASN is non-nil) their autonomous system.
func Trace(ctx context.Context, p probe.Pinger, name string, dest net.IP, opt Options) (Path, error) {
	if dest == nil {
		return Path{}, fmt.Errorf("pathmon: nil destination")
	}
	def := DefaultOptions()
	if opt.MaxHops <= 0 {
		opt.MaxHops = def.MaxHops
	}
	if opt.ProbesPerHop <= 0 {
		opt.ProbesPerHop = def.ProbesPerHop
	}
	if opt.PerHop <= 0 {
		opt.PerHop = def.PerHop
	}
	if opt.RDNSTimeout <= 0 {
		opt.RDNSTimeout = def.RDNSTimeout
	}

	start := time.Now()
	path := Path{Name: name, Dest: dest.String(), When: start}

	type hopResult struct {
		hop     Hop
		reached bool
	}
	var (
		results     []hopResult
		reachedTTL  = 0
		silentRun   = 0
		lastRespond = 0
	)

	for ttl := 1; ttl <= opt.MaxHops && reachedTTL == 0; ttl += batchTTLs {
		if err := ctx.Err(); err != nil {
			return path, err
		}
		end := ttl + batchTTLs - 1
		if end > opt.MaxHops {
			end = opt.MaxHops
		}
		batch := make([]hopResult, end-ttl+1)
		var wg sync.WaitGroup
		for i := range batch {
			wg.Add(1)
			go func(i, t int) {
				defer wg.Done()
				batch[i] = probeHop(ctx, p, dest, t, opt)
			}(i, ttl+i)
		}
		wg.Wait()

		for _, r := range batch {
			results = append(results, r)
			if r.hop.Responded() {
				silentRun = 0
				lastRespond = r.hop.TTL
			} else {
				silentRun++
			}
			if r.reached {
				reachedTTL = r.hop.TTL
				break
			}
			if lastRespond > 0 && silentRun >= silentStop {
				reachedTTL = -1 // sentinel: stop without reaching
				break
			}
		}
	}

	for _, r := range results {
		if reachedTTL > 0 && r.hop.TTL > reachedTTL {
			break
		}
		path.Hops = append(path.Hops, r.hop)
	}
	path.Reached = reachedTTL > 0
	// Drop trailing silent hops when we gave up: they carry no information.
	if !path.Reached {
		for len(path.Hops) > 0 && !path.Hops[len(path.Hops)-1].Responded() {
			path.Hops = path.Hops[:len(path.Hops)-1]
		}
	}

	if opt.Enrich {
		enrich(ctx, path.Hops, opt)
	}
	path.ASPath = ComputeASPath(path.Hops)
	path.Elapsed = time.Since(start)
	return path, ctx.Err()
}

// probeHop sends opt.ProbesPerHop probes at one TTL and summarises the replies.
func probeHop(ctx context.Context, p probe.Pinger, dest net.IP, ttl int, opt Options) (out struct {
	hop     Hop
	reached bool
}) {
	h := Hop{TTL: ttl}
	var sum time.Duration
	for i := 0; i < opt.ProbesPerHop; i++ {
		if ctx.Err() != nil {
			break
		}
		h.Sent++
		pctx, cancel := context.WithTimeout(ctx, opt.PerHop)
		r, err := p.PingTTL(pctx, dest, ttl, opt.PerHop)
		cancel()
		if err != nil || !(r.OK || r.TTLExpired) || r.Addr == nil {
			continue
		}
		if r.OK {
			out.reached = true
		}
		if h.Addr == "" {
			h.Addr = r.Addr.String()
			h.Private = !probe.IsPublicIP(r.Addr)
		}
		h.Recv++
		sum += r.RTT
		if h.Recv == 1 || r.RTT < h.Min {
			h.Min = r.RTT
		}
		if r.RTT > h.Max {
			h.Max = r.RTT
		}
	}
	if h.Sent > 0 {
		h.Loss = float64(h.Sent-h.Recv) / float64(h.Sent)
	}
	if h.Recv > 0 {
		h.Avg = sum / time.Duration(h.Recv)
	}
	out.hop = h
	return out
}

// enrich annotates responding hops with reverse DNS and ASN, with bounded
// parallelism so a slow resolver does not stall the whole trace for long.
func enrich(ctx context.Context, hops []Hop, opt Options) {
	sem := make(chan struct{}, enrichParallel)
	var wg sync.WaitGroup
	res := &net.Resolver{}
	for i := range hops {
		if !hops[i].Responded() {
			continue
		}
		wg.Add(1)
		go func(h *Hop) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ip := net.ParseIP(h.Addr)
			if ip == nil {
				return
			}
			if opt.ASN != nil && !h.Private {
				if info, err := opt.ASN.Lookup(ctx, ip); err == nil {
					h.ASN, h.ASName = info.ASN, info.Name
				}
			}
			rctx, cancel := context.WithTimeout(ctx, opt.RDNSTimeout)
			defer cancel()
			if names, err := res.LookupAddr(rctx, h.Addr); err == nil && len(names) > 0 {
				h.Host = strings.TrimSuffix(names[0], ".")
			}
		}(&hops[i])
	}
	wg.Wait()
}

// ComputeASPath returns the sequence of public ASNs along the hops with
// consecutive repeats collapsed (so "6128 6128 3356 13335 13335" → 6128, 3356,
// 13335). Hops with unknown ASN are skipped.
func ComputeASPath(hops []Hop) []int {
	var out []int
	for _, h := range hops {
		if h.ASN <= 0 {
			continue
		}
		if len(out) == 0 || out[len(out)-1] != h.ASN {
			out = append(out, h.ASN)
		}
	}
	return out
}

// Segment labels which part of the path a hop belongs to.
type Segment int

const (
	SegmentNone        Segment = iota // silent hop with no context
	SegmentLAN                        // inside the home/office network (private addresses)
	SegmentISP                        // the access provider's network
	SegmentTransit                    // intermediate carriers / peering
	SegmentDestination                // the destination host itself
)

func (s Segment) String() string {
	switch s {
	case SegmentLAN:
		return "LAN"
	case SegmentISP:
		return "ISP"
	case SegmentTransit:
		return "Transit"
	case SegmentDestination:
		return "Destination"
	default:
		return "—"
	}
}

// Segments classifies every hop: private addresses before the first public hop
// are LAN; the ISP segment runs from the first public hop through the last hop
// carrying the ISP's ASN (the first *known* ASN at or after the first public
// hop — edge routers often have no ASN mapping of their own); the final hop of
// a reached path is Destination; everything else is Transit. A silent hop
// inherits the segment of the hop before it.
func Segments(p Path) []Segment {
	out := make([]Segment, len(p.Hops))
	firstPublic := -1
	for i, h := range p.Hops {
		if h.Responded() && !h.Private {
			firstPublic = i
			break
		}
	}
	// The ISP's ASN is the first known one at/after the first public hop, and
	// the ISP span ends at the last hop carrying it.
	ispASN, ispEnd := 0, firstPublic
	if firstPublic >= 0 {
		for i := firstPublic; i < len(p.Hops); i++ {
			if a := p.Hops[i].ASN; a > 0 {
				ispASN = a
				break
			}
		}
		if ispASN > 0 {
			for i := firstPublic; i < len(p.Hops); i++ {
				if p.Hops[i].ASN == ispASN {
					ispEnd = i
				}
			}
		}
	}
	prev := SegmentNone
	for i, h := range p.Hops {
		var seg Segment
		switch {
		case p.Reached && i == len(p.Hops)-1:
			seg = SegmentDestination
		case !h.Responded():
			seg = prev
			if seg == SegmentNone && (firstPublic < 0 || i < firstPublic) {
				seg = SegmentLAN
			}
		case firstPublic < 0 || i < firstPublic:
			seg = SegmentLAN
		case i <= ispEnd:
			seg = SegmentISP
		default:
			seg = SegmentTransit
		}
		out[i] = seg
		prev = seg
	}
	return out
}

// Diagnosis names the hop where degradation is introduced, if any.
type Diagnosis struct {
	HopIndex int     // index into Path.Hops, -1 when the path looks clean
	Reason   string  // plain-language explanation
	Segment  Segment // which part of the path that hop belongs to
}

// FirstDegradedHop finds the first hop that introduces *real* degradation:
//
//   - Loss at a hop counts only if loss persists at every later responding hop
//     (loss that vanishes downstream is ICMP rate limiting, not packet loss).
//   - An RTT increase of at least jumpThreshold over the previous responding hop
//     counts only if later responding hops stay at that level (a spike confined
//     to one router is de-prioritised ICMP generation, not path latency).
//
// Loss is checked before latency because loss is the more damaging symptom.
func FirstDegradedHop(p Path, lossThreshold float64, jumpThreshold time.Duration) Diagnosis {
	none := Diagnosis{HopIndex: -1}
	segs := Segments(p)

	// Indices of responding hops, in order.
	var resp []int
	for i, h := range p.Hops {
		if h.Responded() {
			resp = append(resp, i)
		}
	}
	if len(resp) == 0 {
		return none
	}

	// Rule (a): persistent loss.
	if lossThreshold > 0 {
		for k, i := range resp {
			h := p.Hops[i]
			if h.Loss < lossThreshold {
				continue
			}
			persists := true
			for _, j := range resp[k+1:] {
				if p.Hops[j].Loss < lossThreshold {
					persists = false
					break
				}
			}
			if persists {
				return Diagnosis{
					HopIndex: i,
					Segment:  segs[i],
					Reason: fmt.Sprintf("%.0f%% packet loss begins at hop %d (%s) and persists to the end of the path",
						h.Loss*100, h.TTL, describe(h)),
				}
			}
		}
	}

	// Rule (b): persistent latency jump.
	if jumpThreshold > 0 {
		var prevAvg time.Duration
		for k, i := range resp {
			h := p.Hops[i]
			jump := h.Avg - prevAvg
			if jump >= jumpThreshold {
				persists := true
				for _, j := range resp[k+1:] {
					if p.Hops[j].Avg < h.Avg-jumpPersistFuzz {
						persists = false
						break
					}
				}
				if persists {
					return Diagnosis{
						HopIndex: i,
						Segment:  segs[i],
						Reason: fmt.Sprintf("latency jumps by %s at hop %d (%s) and stays high on every later hop",
							jump.Round(time.Millisecond), h.TTL, describe(h)),
					}
				}
			}
			prevAvg = h.Avg
		}
	}
	return none
}

// describe renders a hop as "addr [host] (ASxxxx NAME)" for messages.
func describe(h Hop) string {
	var b strings.Builder
	b.WriteString(h.Addr)
	if h.Host != "" {
		b.WriteString(" ")
		b.WriteString(h.Host)
	}
	if h.ASN > 0 {
		fmt.Fprintf(&b, ", AS%d", h.ASN)
		if h.ASName != "" {
			b.WriteString(" ")
			b.WriteString(h.ASName)
		}
	} else if h.Private {
		b.WriteString(", private")
	}
	return b.String()
}
