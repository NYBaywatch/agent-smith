package dnsprobe

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// startFakeDNS runs a minimal UDP DNS responder that answers every query with
// a single A record (1.2.3.4) for the queried name, after a short delay so the
// measured latency is above the coarse Windows monotonic-clock granularity.
// It returns the listen address and a stop function.
func startFakeDNS(t *testing.T) (string, func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			if len(q) < 12 {
				continue
			}
			// Find the end of the question section (name + qtype + qclass).
			i := 12
			for i < len(q) && q[i] != 0 {
				i += int(q[i]) + 1
			}
			qend := i + 1 + 4
			if qend > len(q) {
				continue
			}
			resp := make([]byte, 0, qend+16)
			resp = append(resp, q[:qend]...)
			binary.BigEndian.PutUint16(resp[2:], 0x8180) // response, RD, RA
			binary.BigEndian.PutUint16(resp[6:], 1)      // ANCOUNT
			binary.BigEndian.PutUint16(resp[8:], 0)
			binary.BigEndian.PutUint16(resp[10:], 0)
			resp = append(resp, 0xc0, 0x0c) // pointer to the question name
			resp = append(resp, 0, 1, 0, 1) // A, IN
			resp = append(resp, 0, 0, 0, 60)
			resp = append(resp, 0, 4, 1, 2, 3, 4)
			time.Sleep(5 * time.Millisecond)
			_, _ = pc.WriteTo(resp, addr)
		}
	}()
	return pc.LocalAddr().String(), func() { pc.Close(); <-done }
}

func TestMeasureAuthoritative(t *testing.T) {
	addr, stop := startFakeDNS(t)
	defer stop()
	host, port, _ := net.SplitHostPort(addr)

	origNS, origHost := lookupNS, lookupHost
	defer func() { lookupNS, lookupHost = origNS, origHost }()

	lookupNS = func(_ context.Context, domain string) ([]*net.NS, error) {
		switch domain {
		case "example.com":
			return []*net.NS{{Host: "ns1.example.com."}, {Host: "ns2.example.com."}, {Host: "ns3.example.com."}}, nil
		case "two.test":
			return []*net.NS{{Host: "a.two.test."}, {Host: "dead.two.test."}}, nil
		default:
			return nil, errors.New("no such domain")
		}
	}
	lookupHost = func(_ context.Context, h string) ([]string, error) {
		if h == "dead.two.test" {
			return nil, errors.New("ns unresolvable")
		}
		return []string{host}, nil
	}

	// The fake responder listens on an ephemeral port; make resolverFor's
	// target use it by rewriting ":53" in Addr via lookupHost returning the
	// host and swapping the port through the resolver hook below.
	origResolver := resolverForHook
	resolverForHook = func(a string, d time.Duration) *net.Resolver {
		h, _, _ := net.SplitHostPort(a)
		return resolverFor(net.JoinHostPort(h, port), d)
	}
	defer func() { resolverForHook = origResolver }()

	res := MeasureAuthoritative(context.Background(), []string{"example.com", "two.test", "missing.invalid"}, 2*time.Second)
	if len(res) != 5 {
		t.Fatalf("got %d results: %+v", len(res), res)
	}
	// example.com: capped at 2 nameservers, both OK with latency > 0.
	ex := filter(res, "example.com")
	if len(ex) != 2 {
		t.Fatalf("example.com results = %+v", ex)
	}
	for _, r := range ex {
		if !r.OK || r.Latency <= 0 || r.Err != "" || !strings.HasPrefix(r.NS, "ns") || !strings.HasPrefix(r.Addr, host+":") {
			t.Errorf("bad result %+v", r)
		}
	}
	// two.test: one OK, one whose NS could not be resolved.
	tw := filter(res, "two.test")
	if len(tw) != 2 || !tw[0].OK || tw[1].OK || tw[1].Err != "ns unresolvable" || tw[1].Addr != "" {
		t.Errorf("two.test results = %+v", tw)
	}
	// NS lookup failure → single failed result.
	mi := filter(res, "missing.invalid")
	if len(mi) != 1 || mi[0].OK || mi[0].Err != "no such domain" || mi[0].NS != "" {
		t.Errorf("missing.invalid results = %+v", mi)
	}
}

func TestMeasureAuthoritativeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	orig := lookupNS
	defer func() { lookupNS = orig }()
	lookupNS = func(ctx context.Context, _ string) ([]*net.NS, error) { return nil, ctx.Err() }
	res := MeasureAuthoritative(ctx, []string{"a.test", "b.test"}, time.Second)
	if len(res) != 2 {
		t.Fatalf("results = %+v", res)
	}
	for _, r := range res {
		if r.OK || r.Err == "" {
			t.Errorf("expected failure, got %+v", r)
		}
	}
}

func TestSystemResolvers(t *testing.T) {
	// Must not panic and must return only parseable addresses on any platform.
	for _, a := range SystemResolvers() {
		if net.ParseIP(a) == nil {
			t.Errorf("non-IP resolver %q", a)
		}
	}
}

func filter(rs []AuthResult, domain string) []AuthResult {
	var out []AuthResult
	for _, r := range rs {
		if r.Domain == domain {
			out = append(out, r)
		}
	}
	return out
}
