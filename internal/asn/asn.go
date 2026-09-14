// Package asn maps IP addresses to the autonomous system (network operator)
// that announces them, using Team Cymru's public IP-to-ASN DNS service. Knowing
// which network each traceroute hop belongs to is what turns a list of router
// addresses into a readable story: "your ISP → transit carrier → Cloudflare".
//
// Lookups are plain DNS TXT queries (no API key), cached in memory, and
// short-circuited for private/reserved addresses so LAN hops never trigger a
// network call.
package asn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NYBaywatch/agent-smith/internal/probe"
)

// Info describes the autonomous system announcing an address.
type Info struct {
	ASN     int    // 0 when unknown or non-public
	Name    string // short AS name (e.g. "CABLE-NET-1"); "private"/"reserved" for non-public IPs
	Prefix  string // announced prefix covering the IP, e.g. "67.80.0.0/13"
	Country string // registry country code, e.g. "US"
}

const (
	originV4Zone = "origin.asn.cymru.com"
	originV6Zone = "origin6.asn.cymru.com"
	asNameZone   = "asn.cymru.com"

	lookupTimeout = 2 * time.Second
	negativeTTL   = 10 * time.Minute
)

// ErrNoAnswer is returned when Team Cymru has no origin record for an address.
var ErrNoAnswer = errors.New("asn: no origin record")

type cacheEntry struct {
	info    Info
	err     error
	expires time.Time // zero = never (positive entries are kept for the process lifetime)
}

// Resolver performs cached IP → AS lookups. It is safe for concurrent use.
type Resolver struct {
	mu    sync.Mutex
	cache map[string]cacheEntry
	names map[int]string // ASN → name cache

	// lookupTXT performs the underlying DNS TXT query; tests inject a fake.
	lookupTXT func(ctx context.Context, name string) ([]string, error)
	now       func() time.Time
}

// NewResolver returns a Resolver backed by the system DNS resolver.
func NewResolver() *Resolver {
	r := &net.Resolver{}
	return &Resolver{
		cache: make(map[string]cacheEntry),
		names: make(map[int]string),
		lookupTXT: func(ctx context.Context, name string) ([]string, error) {
			lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
			defer cancel()
			return r.LookupTXT(lctx, name)
		},
		now: time.Now,
	}
}

// Lookup returns the AS information for ip. Private, loopback, link-local and
// other non-routable addresses return Info{Name: "private"} (or "reserved")
// immediately without a network call. Failures are negatively cached for ten
// minutes so a flaky resolver does not hammer the service.
func (r *Resolver) Lookup(ctx context.Context, ip net.IP) (Info, error) {
	if ip == nil {
		return Info{}, errors.New("asn: nil ip")
	}
	if !probe.IsPublicIP(ip) {
		return Info{Name: nonPublicLabel(ip)}, nil
	}
	key := ip.String()

	r.mu.Lock()
	if e, ok := r.cache[key]; ok && (e.expires.IsZero() || r.now().Before(e.expires)) {
		r.mu.Unlock()
		return e.info, e.err
	}
	r.mu.Unlock()

	info, err := r.fetch(ctx, ip)

	r.mu.Lock()
	if err != nil {
		// Do not poison the cache with a caller cancellation.
		if ctx.Err() == nil {
			r.cache[key] = cacheEntry{err: err, expires: r.now().Add(negativeTTL)}
		}
	} else {
		r.cache[key] = cacheEntry{info: info}
	}
	r.mu.Unlock()
	return info, err
}

// fetch performs the two-step Cymru lookup (origin, then AS name).
func (r *Resolver) fetch(ctx context.Context, ip net.IP) (Info, error) {
	var name string
	if v4 := ip.To4(); v4 != nil {
		name = ReverseV4(v4) + "." + originV4Zone
	} else {
		name = ReverseV6(ip) + "." + originV6Zone
	}
	txts, err := r.lookupTXT(ctx, name)
	if err != nil {
		return Info{}, fmt.Errorf("asn: origin lookup %s: %w", ip, err)
	}
	if len(txts) == 0 {
		return Info{}, ErrNoAnswer
	}
	info, err := ParseOrigin(txts[0])
	if err != nil {
		return Info{}, err
	}
	info.Name = r.asName(ctx, info.ASN)
	return info, nil
}

// asName resolves (and caches) the short name for an AS number. Failures
// yield "" rather than an error: the number alone is still useful.
func (r *Resolver) asName(ctx context.Context, asn int) string {
	if asn <= 0 {
		return ""
	}
	r.mu.Lock()
	if n, ok := r.names[asn]; ok {
		r.mu.Unlock()
		return n
	}
	r.mu.Unlock()

	txts, err := r.lookupTXT(ctx, fmt.Sprintf("AS%d.%s", asn, asNameZone))
	if err != nil || len(txts) == 0 {
		return ""
	}
	n := ParseASName(txts[0])
	r.mu.Lock()
	r.names[asn] = n
	r.mu.Unlock()
	return n
}

// ParseOrigin parses a Team Cymru origin TXT record such as
// "6128 | 67.80.0.0/13 | US | arin | 1998-01-01". When several ASNs announce
// the prefix ("6128 3356 | ...") the first is used. Name is left empty.
func ParseOrigin(txt string) (Info, error) {
	fields := splitPipe(txt)
	if len(fields) < 2 {
		return Info{}, fmt.Errorf("asn: malformed origin record %q", txt)
	}
	asnField := strings.Fields(fields[0])
	if len(asnField) == 0 {
		return Info{}, fmt.Errorf("asn: malformed origin record %q", txt)
	}
	n, err := strconv.Atoi(asnField[0])
	if err != nil || n <= 0 {
		return Info{}, fmt.Errorf("asn: bad AS number in %q", txt)
	}
	info := Info{ASN: n, Prefix: fields[1]}
	if len(fields) > 2 {
		info.Country = fields[2]
	}
	return info, nil
}

// ParseASName extracts the short AS name from a Team Cymru AS TXT record such
// as "6128 | US | arin | 1998-01-01 | CABLE-NET-1, US" → "CABLE-NET-1".
// Returns "" when the record has no name field.
func ParseASName(txt string) string {
	fields := splitPipe(txt)
	if len(fields) < 5 {
		return ""
	}
	name := fields[4]
	if i := strings.Index(name, ","); i >= 0 {
		name = name[:i]
	}
	return strings.TrimSpace(name)
}

// ReverseV4 returns the octet-reversed form of an IPv4 address ("1.0.0.1" →
// "1.0.0.1", "67.83.1.2" → "2.1.83.67"), as used by the origin zone.
func ReverseV4(ip net.IP) string {
	v4 := ip.To4()
	if v4 == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", v4[3], v4[2], v4[1], v4[0])
}

// ReverseV6 returns the nibble-reversed, dot-separated form of an IPv6 address
// (as in ip6.arpa), e.g. 2001:db8::1 → "1.0.0.0.…0.8.b.d.0.1.0.0.2".
func ReverseV6(ip net.IP) string {
	v6 := ip.To16()
	if v6 == nil || ip.To4() != nil {
		return ""
	}
	const hex = "0123456789abcdef"
	var b strings.Builder
	b.Grow(63)
	for i := len(v6) - 1; i >= 0; i-- {
		b.WriteByte(hex[v6[i]&0x0f])
		b.WriteByte('.')
		b.WriteByte(hex[v6[i]>>4])
		if i > 0 {
			b.WriteByte('.')
		}
	}
	return b.String()
}

func splitPipe(s string) []string {
	parts := strings.Split(s, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func nonPublicLabel(ip net.IP) string {
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return "private"
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64 { // CGNAT 100.64/10
		return "private"
	}
	return "reserved"
}
