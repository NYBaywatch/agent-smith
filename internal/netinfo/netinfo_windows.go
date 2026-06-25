//go:build windows

package netinfo

import (
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// GetAdaptersAddresses flags (ipdef.h / iptypes.h).
const (
	gaaFlagSkipAnycast     = 0x0002
	gaaFlagSkipMulticast   = 0x0004
	gaaFlagSkipDNSServer   = 0x0008
	gaaFlagIncludeGateways = 0x0080
)

// IANA ifType values (ipifcons.h).
const (
	ifTypeEthernet = 6
	ifTypeWifi     = 71
	ifTypeLoopback = 24
)

const ifOperStatusUp = 1

func collect() (*Info, error) {
	adapters, err := adapterAddresses()
	if err != nil {
		return nil, err
	}

	info := &Info{}
	gateways := map[uint32]net.IP{} // ifIndex -> gateway IP

	// firstGW remembers the first up adapter that has a gateway, used as a
	// fallback when the OS best-interface lookup is unavailable.
	var fallbackIdx uint32
	haveFallback := false

	for a := adapters; a != nil; a = a.Next {
		if a.IfType == ifTypeLoopback {
			continue
		}
		iface := Interface{
			Index:       a.IfIndex,
			Name:        windows.UTF16PtrToString(a.FriendlyName),
			Description: windows.UTF16PtrToString(a.Description),
			Media:       mediaFromIfType(a.IfType),
			Up:          a.OperStatus == ifOperStatusUp,
			LinkMbps:    a.TransmitLinkSpeed / 1_000_000,
			MTU:         a.Mtu,
		}
		fillCounters(&iface)
		info.Interfaces = append(info.Interfaces, iface)

		if a.OperStatus == ifOperStatusUp && a.FirstGatewayAddress != nil {
			if gw := sockaddrIP(a.FirstGatewayAddress.Address); gw != nil {
				gateways[a.IfIndex] = gw
				if !haveFallback {
					fallbackIdx, haveFallback = a.IfIndex, true
				}
			}
		}
	}

	// Auto-detect the active egress adapter: ask the OS which interface it would
	// use to reach the public internet (this picks Wi-Fi vs Ethernet correctly
	// even when both are connected). Fall back to the first adapter with a gateway.
	activeIdx, ok := bestInterfaceIndex()
	if _, hasGW := gateways[activeIdx]; !ok || !hasGW {
		activeIdx, ok = fallbackIdx, haveFallback
	}
	if ok {
		for i := range info.Interfaces {
			if info.Interfaces[i].Index == activeIdx {
				info.Active = &info.Interfaces[i]
				break
			}
		}
		info.GatewayIP = gateways[activeIdx]
		if info.Active != nil && info.Active.Media == MediaWireless {
			if w := collectWiFi(); w != nil {
				info.WiFi = w
			}
		}
	}

	return info, nil
}

// bestInterfaceIndex asks Windows which interface it would use to reach a public
// internet address — i.e. the active egress adapter (Wi-Fi or Ethernet).
func bestInterfaceIndex() (uint32, bool) {
	var idx uint32
	sa := &windows.SockaddrInet4{Addr: [4]byte{8, 8, 8, 8}}
	if err := windows.GetBestInterfaceEx(sa, &idx); err != nil {
		return 0, false
	}
	return idx, true
}

func mediaFromIfType(t uint32) MediaType {
	switch t {
	case ifTypeEthernet:
		return MediaWired
	case ifTypeWifi:
		return MediaWireless
	default:
		return MediaOther
	}
}

// adapterAddresses returns the head of the adapter list (IPv4).
func adapterAddresses() (*windows.IpAdapterAddresses, error) {
	const flags = gaaFlagIncludeGateways | gaaFlagSkipAnycast | gaaFlagSkipMulticast | gaaFlagSkipDNSServer
	size := uint32(15000)
	for attempt := 0; attempt < 4; attempt++ {
		buf := make([]byte, size)
		err := windows.GetAdaptersAddresses(
			windows.AF_INET, flags, 0,
			(*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size,
		)
		if err == nil {
			return (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), nil
		}
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue // size now holds the required length; retry
		}
		return nil, fmt.Errorf("GetAdaptersAddresses: %w", err)
	}
	return nil, fmt.Errorf("GetAdaptersAddresses: buffer kept overflowing")
}

// fillCounters populates error/discard/octet counters via GetIfEntry2Ex.
func fillCounters(iface *Interface) {
	row := windows.MibIfRow2{InterfaceIndex: iface.Index}
	if err := windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row); err != nil {
		return // counters simply remain zero if unavailable
	}
	iface.InErrors = row.InErrors
	iface.OutErrors = row.OutErrors
	iface.InDiscards = row.InDiscards
	iface.OutDiscards = row.OutDiscards
	iface.InOctets = row.InOctets
	iface.OutOctets = row.OutOctets
	if iface.LinkMbps == 0 {
		iface.LinkMbps = row.TransmitLinkSpeed / 1_000_000
	}
}

// sockaddrIP extracts an IPv4 address from a Windows SocketAddress.
func sockaddrIP(sa windows.SocketAddress) net.IP {
	if sa.Sockaddr == nil {
		return nil
	}
	if sa.Sockaddr.Addr.Family != windows.AF_INET {
		return nil
	}
	p := (*windows.RawSockaddrInet4)(unsafe.Pointer(sa.Sockaddr))
	return net.IPv4(p.Addr[0], p.Addr[1], p.Addr[2], p.Addr[3])
}
