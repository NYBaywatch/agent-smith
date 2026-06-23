package probe

import (
	"net"
	"testing"
	"time"
)

func TestIsPublicIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"192.168.1.1", false},  // private
		{"10.0.0.1", false},     // private
		{"172.16.5.4", false},   // private
		{"127.0.0.1", false},    // loopback
		{"169.254.1.1", false},  // link-local
		{"100.64.0.1", false},   // CGNAT
		{"100.127.255.1", false}, // CGNAT upper
		{"100.128.0.1", true},   // just outside CGNAT
		{"0.0.0.0", false},      // unspecified
		{"0.1.2.3", false},      // 0.0.0.0/8 "this network"
		{"224.0.0.1", false},    // multicast
		{"240.0.0.1", false},    // reserved 240/4
		{"255.255.255.255", false}, // broadcast
		{"192.0.2.5", false},    // TEST-NET-1
		{"198.18.0.1", false},   // benchmarking
		{"198.51.100.7", false}, // TEST-NET-2
		{"203.0.113.9", false},  // TEST-NET-3
		{"203.0.114.9", true},   // adjacent to TEST-NET-3, routable
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if got := IsPublicIP(ip); got != c.want {
			t.Errorf("IsPublicIP(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
	if IsPublicIP(nil) {
		t.Error("IsPublicIP(nil) should be false")
	}
}

func TestFirstPublicHop(t *testing.T) {
	hops := []Hop{
		{TTL: 1, Addr: net.ParseIP("192.168.1.1"), OK: true, RTT: time.Millisecond},
		{TTL: 2, Addr: nil, OK: false}, // silent hop
		{TTL: 3, Addr: net.ParseIP("100.64.0.1"), OK: true},  // CGNAT, still "inside" ISP NAT
		{TTL: 4, Addr: net.ParseIP("67.59.233.1"), OK: true}, // first real public hop
		{TTL: 5, Addr: net.ParseIP("8.8.8.8"), OK: true},
	}
	h := FirstPublicHop(hops)
	if h == nil {
		t.Fatal("expected a public hop")
	}
	if h.Addr.String() != "67.59.233.1" {
		t.Fatalf("first public hop = %v, want 67.59.233.1", h.Addr)
	}
}

func TestFirstPublicHopNone(t *testing.T) {
	hops := []Hop{
		{TTL: 1, Addr: net.ParseIP("192.168.1.1"), OK: true},
		{TTL: 2, Addr: nil, OK: false},
	}
	if h := FirstPublicHop(hops); h != nil {
		t.Fatalf("expected no public hop, got %v", h.Addr)
	}
}
