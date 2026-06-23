// Package probe sends ICMP echo probes and TTL-limited probes (for traceroute).
//
// On Windows it uses the Windows ICMP API (IcmpCreateFile / IcmpSendEcho in
// iphlpapi.dll), which works for standard, non-administrator users — unlike raw
// ICMP sockets, which require elevation on Windows. On other platforms a
// golang.org/x/net/icmp based fallback is used so the rest of the codebase
// builds and runs in CI.
package probe

import (
	"context"
	"net"
	"time"
)

// Result describes the outcome of a single probe.
type Result struct {
	Addr   net.IP        // responding address (hop address for TTL-expired replies)
	RTT    time.Duration // round-trip time when OK
	OK     bool          // true when an echo reply was received
	Status string        // human-readable status (e.g. "ok", "timeout", "ttl-expired")
	// TTLExpired is true when the probe elicited a "TTL expired in transit"
	// response — i.e. Addr is an intermediate router (used by traceroute).
	TTLExpired bool
}

// Pinger sends ICMP probes. Implementations must be safe for concurrent use.
type Pinger interface {
	// Ping sends a single echo request to ip and waits up to timeout for a reply.
	Ping(ctx context.Context, ip net.IP, timeout time.Duration) (Result, error)
	// PingTTL sends a single echo request with the given IP TTL. A TTL-expired
	// reply yields Result.TTLExpired = true with the intermediate hop in Addr.
	PingTTL(ctx context.Context, ip net.IP, ttl int, timeout time.Duration) (Result, error)
	// Close releases any resources held by the pinger.
	Close() error
}

// New returns the platform-appropriate Pinger.
func New() (Pinger, error) { return newPinger() }

// payload is the request data sent with each echo (mirrors what ping.exe sends).
var payload = []byte("AgentSmith-probe-0123456789ABCDEF")
