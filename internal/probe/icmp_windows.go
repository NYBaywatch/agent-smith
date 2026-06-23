//go:build windows

package probe

import (
	"context"
	"fmt"
	"net"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows IP status codes returned by the ICMP API (subset).
// See https://learn.microsoft.com/windows/win32/api/ipexport/
const (
	ipSuccess            = 0
	ipBufTooSmall        = 11001
	ipDestNetUnreachable = 11002
	ipDestHostUnreach    = 11003
	ipDestProtUnreach    = 11004
	ipDestPortUnreach    = 11005
	ipReqTimedOut        = 11010
	ipTTLExpiredTransit  = 11013
)

var (
	modIphlpapi         = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreateFile  = modIphlpapi.NewProc("IcmpCreateFile")
	procIcmpCloseHandle = modIphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho    = modIphlpapi.NewProc("IcmpSendEcho")
)

// ipOptionInformation mirrors IP_OPTION_INFORMATION (64-bit layout).
type ipOptionInformation struct {
	TTL         uint8
	Tos         uint8
	Flags       uint8
	OptionsSize uint8
	OptionsData uintptr
}

// icmpEchoReply mirrors ICMP_ECHO_REPLY (64-bit layout).
type icmpEchoReply struct {
	Address       uint32
	Status        uint32
	RoundTripTime uint32
	DataSize      uint16
	Reserved      uint16
	Data          uintptr
	Options       ipOptionInformation
}

// winPinger is the Windows ICMP-API backed Pinger. It is stateless across calls
// (a fresh ICMP handle is opened per probe) so it is safe for concurrent use.
type winPinger struct{}

func newPinger() (Pinger, error) {
	if err := procIcmpCreateFile.Find(); err != nil {
		return nil, fmt.Errorf("iphlpapi IcmpCreateFile unavailable: %w", err)
	}
	return &winPinger{}, nil
}

func (p *winPinger) Close() error { return nil }

func (p *winPinger) Ping(ctx context.Context, ip net.IP, timeout time.Duration) (Result, error) {
	return p.send(ctx, ip, 0, timeout)
}

func (p *winPinger) PingTTL(ctx context.Context, ip net.IP, ttl int, timeout time.Duration) (Result, error) {
	return p.send(ctx, ip, ttl, timeout)
}

func (p *winPinger) send(ctx context.Context, ip net.IP, ttl int, timeout time.Duration) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	v4 := ip.To4()
	if v4 == nil {
		return Result{}, fmt.Errorf("probe: only IPv4 is supported (got %v)", ip)
	}
	// Cap timeout by the context deadline if one is set.
	if dl, ok := ctx.Deadline(); ok {
		if rem := time.Until(dl); rem < timeout {
			timeout = rem
		}
	}
	if timeout <= 0 {
		return Result{Status: "timeout"}, nil
	}

	handle, _, _ := procIcmpCreateFile.Call()
	if handle == 0 || handle == uintptr(^uintptr(0)) {
		return Result{}, fmt.Errorf("probe: IcmpCreateFile failed")
	}
	defer procIcmpCloseHandle.Call(handle)

	dest := uint32(v4[0]) | uint32(v4[1])<<8 | uint32(v4[2])<<16 | uint32(v4[3])<<24

	var optPtr uintptr
	if ttl > 0 {
		opt := ipOptionInformation{TTL: uint8(ttl)}
		optPtr = uintptr(unsafe.Pointer(&opt))
	}

	// Reply buffer: ICMP_ECHO_REPLY + payload + room for an ICMP error message.
	replyBuf := make([]byte, unsafe.Sizeof(icmpEchoReply{})+uintptr(len(payload))+64)
	timeoutMs := uint32(timeout / time.Millisecond)
	if timeoutMs == 0 {
		timeoutMs = 1
	}

	start := time.Now()
	ret, _, callErr := procIcmpSendEcho.Call(
		handle,
		uintptr(dest),
		uintptr(unsafe.Pointer(&payload[0])),
		uintptr(len(payload)),
		optPtr,
		uintptr(unsafe.Pointer(&replyBuf[0])),
		uintptr(len(replyBuf)),
		uintptr(timeoutMs),
	)
	elapsed := time.Since(start)
	// Keep opt alive until after the call.
	if ttl > 0 {
		_ = optPtr
	}

	if ret == 0 {
		return classifyError(callErr), nil
	}

	reply := (*icmpEchoReply)(unsafe.Pointer(&replyBuf[0]))
	addr := net.IPv4(byte(reply.Address), byte(reply.Address>>8), byte(reply.Address>>16), byte(reply.Address>>24))

	// Prefer the kernel-reported RTT (ms resolution); fall back to measured
	// wall-clock for sub-millisecond LAN replies the API reports as 0 ms.
	rtt := time.Duration(reply.RoundTripTime) * time.Millisecond
	if reply.RoundTripTime == 0 {
		rtt = elapsed
	}

	switch reply.Status {
	case ipSuccess:
		return Result{Addr: addr, RTT: rtt, OK: true, Status: "ok"}, nil
	case ipTTLExpiredTransit:
		return Result{Addr: addr, RTT: rtt, OK: false, TTLExpired: true, Status: "ttl-expired"}, nil
	case ipReqTimedOut:
		return Result{Status: "timeout"}, nil
	case ipDestNetUnreachable, ipDestHostUnreach, ipDestProtUnreach, ipDestPortUnreach:
		return Result{Addr: addr, Status: "unreachable"}, nil
	default:
		return Result{Addr: addr, Status: fmt.Sprintf("ip-status-%d", reply.Status)}, nil
	}
}

func classifyError(callErr error) Result {
	if errno, ok := callErr.(windows.Errno); ok {
		switch uint32(errno) {
		case ipReqTimedOut:
			return Result{Status: "timeout"}
		case ipDestNetUnreachable, ipDestHostUnreach, ipDestProtUnreach, ipDestPortUnreach:
			return Result{Status: "unreachable"}
		case ipSuccess:
			// ret==0 with no error: treat as timeout.
			return Result{Status: "timeout"}
		default:
			return Result{Status: fmt.Sprintf("err-%d", uint32(errno))}
		}
	}
	return Result{Status: "timeout"}
}
