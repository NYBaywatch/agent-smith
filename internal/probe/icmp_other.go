//go:build !windows

package probe

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// otherPinger is a portable fallback used on non-Windows platforms (primarily so
// the project builds and tests in Linux CI, and is usable for local dev on
// macOS/Linux). It uses an unprivileged "udp4" ICMP socket where the OS allows
// it (Linux net.ipv4.ping_group_range, macOS by default).
type otherPinger struct{ id int }

func newPinger() (Pinger, error) { return &otherPinger{id: os.Getpid() & 0xffff}, nil }

func (p *otherPinger) Close() error { return nil }

func (p *otherPinger) Ping(ctx context.Context, ip net.IP, timeout time.Duration) (Result, error) {
	return p.send(ctx, ip, 0, timeout)
}

func (p *otherPinger) PingTTL(ctx context.Context, ip net.IP, ttl int, timeout time.Duration) (Result, error) {
	return p.send(ctx, ip, ttl, timeout)
}

func (p *otherPinger) send(ctx context.Context, ip net.IP, ttl int, timeout time.Duration) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	v4 := ip.To4()
	if v4 == nil {
		return Result{}, fmt.Errorf("probe: only IPv4 is supported (got %v)", ip)
	}
	if dl, ok := ctx.Deadline(); ok {
		if rem := time.Until(dl); rem < timeout {
			timeout = rem
		}
	}
	if timeout <= 0 {
		return Result{Status: "timeout"}, nil
	}

	c, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		return Result{}, fmt.Errorf("probe: listen icmp: %w", err)
	}
	defer c.Close()

	pc := c.IPv4PacketConn()
	if ttl > 0 {
		_ = pc.SetTTL(ttl)
	}

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{ID: p.id, Seq: 1, Data: payload},
	}
	wb, err := msg.Marshal(nil)
	if err != nil {
		return Result{}, err
	}

	start := time.Now()
	if _, err := c.WriteTo(wb, &net.UDPAddr{IP: ip}); err != nil {
		return Result{}, fmt.Errorf("probe: write: %w", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(timeout))

	rb := make([]byte, 1500)
	n, peer, err := c.ReadFrom(rb)
	if err != nil {
		return Result{Status: "timeout"}, nil
	}
	rtt := time.Since(start)

	rm, err := icmp.ParseMessage(1 /* iana.ProtocolICMP */, rb[:n])
	if err != nil {
		return Result{Status: "parse-error"}, nil
	}

	peerIP := peerToIP(peer)
	switch rm.Type {
	case ipv4.ICMPTypeEchoReply:
		return Result{Addr: peerIP, RTT: rtt, OK: true, Status: "ok"}, nil
	case ipv4.ICMPTypeTimeExceeded:
		return Result{Addr: peerIP, RTT: rtt, TTLExpired: true, Status: "ttl-expired"}, nil
	case ipv4.ICMPTypeDestinationUnreachable:
		return Result{Addr: peerIP, Status: "unreachable"}, nil
	default:
		return Result{Addr: peerIP, Status: fmt.Sprintf("icmp-type-%v", rm.Type)}, nil
	}
}

func peerToIP(addr net.Addr) net.IP {
	switch a := addr.(type) {
	case *net.UDPAddr:
		return a.IP
	case *net.IPAddr:
		return a.IP
	default:
		return nil
	}
}
