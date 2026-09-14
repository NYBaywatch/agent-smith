package asn

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseOrigin(t *testing.T) {
	info, err := ParseOrigin("6128 | 67.80.0.0/13 | US | arin | 1998-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if info.ASN != 6128 || info.Prefix != "67.80.0.0/13" || info.Country != "US" {
		t.Errorf("unexpected %+v", info)
	}
	// Multi-origin: first ASN wins.
	info, err = ParseOrigin("6128 3356 | 67.80.0.0/13 | US | arin | 1998-01-01")
	if err != nil || info.ASN != 6128 {
		t.Errorf("multi-origin: %+v %v", info, err)
	}
	if _, err := ParseOrigin("garbage"); err == nil {
		t.Error("expected error for malformed record")
	}
	if _, err := ParseOrigin("x | 1.2.3.0/24"); err == nil {
		t.Error("expected error for non-numeric ASN")
	}
}

func TestParseASName(t *testing.T) {
	if got := ParseASName("6128 | US | arin | 1998-01-01 | CABLE-NET-1, US"); got != "CABLE-NET-1" {
		t.Errorf("got %q", got)
	}
	if got := ParseASName("13335 | US | arin | 2010-07-14 | CLOUDFLARENET"); got != "CLOUDFLARENET" {
		t.Errorf("got %q", got)
	}
	if got := ParseASName("6128 | US"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestReverseNames(t *testing.T) {
	if got := ReverseV4(net.ParseIP("67.83.1.2")); got != "2.1.83.67" {
		t.Errorf("v4: got %q", got)
	}
	if got := ReverseV4(net.ParseIP("2001:db8::1")); got != "" {
		t.Errorf("v4 of v6 should be empty, got %q", got)
	}
	want := "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2"
	if got := ReverseV6(net.ParseIP("2001:db8::1")); got != want {
		t.Errorf("v6:\n got %q\nwant %q", got, want)
	}
	if got := ReverseV6(net.ParseIP("1.2.3.4")); got != "" {
		t.Errorf("v6 of v4 should be empty, got %q", got)
	}
}

func fakeResolver(fn func(name string) ([]string, error)) (*Resolver, *int32) {
	var calls int32
	r := NewResolver()
	r.lookupTXT = func(_ context.Context, name string) ([]string, error) {
		atomic.AddInt32(&calls, 1)
		return fn(name)
	}
	return r, &calls
}

func TestLookupCacheHit(t *testing.T) {
	r, calls := fakeResolver(func(name string) ([]string, error) {
		switch name {
		case "1.0.0.1.origin.asn.cymru.com":
			return []string{"13335 | 1.0.0.0/24 | AU | apnic | 2011-08-11"}, nil
		case "AS13335.asn.cymru.com":
			return []string{"13335 | US | arin | 2010-07-14 | CLOUDFLARENET, US"}, nil
		}
		return nil, errors.New("unexpected " + name)
	})
	ip := net.ParseIP("1.0.0.1")
	info, err := r.Lookup(context.Background(), ip)
	if err != nil {
		t.Fatal(err)
	}
	if info.ASN != 13335 || info.Name != "CLOUDFLARENET" || info.Prefix != "1.0.0.0/24" || info.Country != "AU" {
		t.Errorf("unexpected %+v", info)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("expected 2 DNS calls (origin + name), got %d", got)
	}
	if _, err := r.Lookup(context.Background(), ip); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Errorf("cache miss: expected 2 calls total, got %d", got)
	}
	// Same ASN, different IP: name is served from the ASN cache (one origin call only).
	r.lookupTXT = func(_ context.Context, name string) ([]string, error) {
		atomic.AddInt32(calls, 1)
		return []string{"13335 | 1.1.1.0/24 | AU | apnic | 2011-08-11"}, nil
	}
	info, err = r.Lookup(context.Background(), net.ParseIP("1.1.1.1"))
	if err != nil || info.Name != "CLOUDFLARENET" {
		t.Errorf("name cache: %+v %v", info, err)
	}
	if got := atomic.LoadInt32(calls); got != 3 {
		t.Errorf("expected 3 calls, got %d", got)
	}
}

func TestLookupPrivateShortCircuit(t *testing.T) {
	r, calls := fakeResolver(func(string) ([]string, error) { return nil, errors.New("must not be called") })
	for _, s := range []string{"192.168.1.1", "10.0.0.1", "127.0.0.1", "100.64.3.4", "fe80::1"} {
		info, err := r.Lookup(context.Background(), net.ParseIP(s))
		if err != nil || info.Name != "private" || info.ASN != 0 {
			t.Errorf("%s: %+v %v", s, info, err)
		}
	}
	info, err := r.Lookup(context.Background(), net.ParseIP("0.1.2.3"))
	if err != nil || info.Name != "reserved" {
		t.Errorf("reserved: %+v %v", info, err)
	}
	if *calls != 0 {
		t.Errorf("expected no DNS calls, got %d", *calls)
	}
}

func TestLookupNegativeCache(t *testing.T) {
	r, calls := fakeResolver(func(string) ([]string, error) { return nil, errors.New("servfail") })
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	ip := net.ParseIP("8.8.8.8")
	if _, err := r.Lookup(context.Background(), ip); err == nil {
		t.Fatal("expected error")
	}
	if _, err := r.Lookup(context.Background(), ip); err == nil {
		t.Fatal("expected cached error")
	}
	if *calls != 1 {
		t.Errorf("negative cache: expected 1 call, got %d", *calls)
	}
	now = now.Add(negativeTTL + time.Second)
	_, _ = r.Lookup(context.Background(), ip)
	if *calls != 2 {
		t.Errorf("negative cache expiry: expected 2 calls, got %d", *calls)
	}
}
