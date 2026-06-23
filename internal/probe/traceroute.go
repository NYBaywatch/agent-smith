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

// IsPublicIP reports whether ip is a routable public address, excluding
// private, loopback, link-local, multicast, CGNAT (100.64/10), and the various
// reserved/special-use IPv4 ranges that can appear as traceroute hops but are
// never a real ISP edge.
func IsPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip.IsPrivate() {
		return false
	}
	v4 := ip.To4()
	if v4 == nil {
		return true // IPv6 global unicast (private/link-local already excluded)
	}
	switch {
	case v4[0] == 0: // 0.0.0.0/8 "this network"
		return false
	case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127: // CGNAT 100.64.0.0/10
		return false
	case v4[0] == 192 && v4[1] == 0 && v4[2] == 2: // TEST-NET-1 192.0.2.0/24
		return false
	case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19): // benchmarking 198.18.0.0/15
		return false
	case v4[0] == 198 && v4[1] == 51 && v4[2] == 100: // TEST-NET-2 198.51.100.0/24
		return false
	case v4[0] == 203 && v4[1] == 0 && v4[2] == 113: // TEST-NET-3 203.0.113.0/24
		return false
	case v4[0] >= 240: // 240.0.0.0/4 reserved (incl. 255.255.255.255)
		return false
	}
	return true
}
