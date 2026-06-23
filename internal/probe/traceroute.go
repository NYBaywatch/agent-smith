package probe

import (
	"context"
	"net"
	"time"
)

// Hop is one step along the path to a destination.
type Hop struct {
	TTL  int
	Addr net.IP
	RTT  time.Duration
	OK   bool // a reply (echo reply or TTL-expired) was received
}

// Traceroute discovers the path to dest by sending probes with increasing TTL,
// up to maxHops. It stops once the destination itself replies. Hops that do not
// respond are included with OK=false.
func Traceroute(ctx context.Context, p Pinger, dest net.IP, maxHops int, perHop time.Duration) ([]Hop, error) {
	if maxHops <= 0 {
		maxHops = 30
	}
	if perHop <= 0 {
		perHop = time.Second
	}
	hops := make([]Hop, 0, maxHops)
	for ttl := 1; ttl <= maxHops; ttl++ {
		if err := ctx.Err(); err != nil {
			return hops, err
		}
		hctx, cancel := context.WithTimeout(ctx, perHop)
		r, err := p.PingTTL(hctx, dest, ttl, perHop)
		cancel()
		h := Hop{TTL: ttl}
		if err == nil && (r.OK || r.TTLExpired) {
			h.Addr = r.Addr
			h.RTT = r.RTT
			h.OK = true
		}
		hops = append(hops, h)
		if err == nil && r.OK {
			break // reached the destination
		}
	}
	return hops, nil
}

// FirstPublicHop returns the first hop along the path whose address is a public
// (non-private, non-loopback) IP — i.e. the ISP access-link edge. Returns nil if
// none is found (e.g. CGNAT-only paths or all hops silent).
func FirstPublicHop(hops []Hop) *Hop {
	for i := range hops {
		h := hops[i]
		if h.OK && h.Addr != nil && IsPublicIP(h.Addr) {
			return &h
		}
	}
	return nil
}

// IsPublicIP reports whether ip is a routable public address (not private,
// loopback, link-local, or CGNAT 100.64/10).
func IsPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	if ip.IsPrivate() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// CGNAT 100.64.0.0/10
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
	}
	return true
}
