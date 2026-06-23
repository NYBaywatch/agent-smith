// Package netinfo discovers the local network topology that Agent Smith needs to
// localize bottlenecks: the default gateway (so it can be pinged as the LAN
// reference), the active egress interface and its media type (wired vs Wi-Fi),
// link speed, MTU and error/discard counters, and — when on Wi-Fi — the RSSI,
// link rate and SSID.
//
// The rich implementation is Windows-only (IP Helper API + WLAN API). Other
// platforms get a best-effort fallback so the project builds and runs in CI.
package netinfo

import (
	"net"
)

// MediaType classifies how the active interface connects.
type MediaType int

const (
	MediaUnknown MediaType = iota
	MediaWired
	MediaWireless
	MediaOther
)

func (m MediaType) String() string {
	switch m {
	case MediaWired:
		return "Wired"
	case MediaWireless:
		return "Wi-Fi"
	case MediaOther:
		return "Other"
	default:
		return "Unknown"
	}
}

// Interface describes one network adapter and its counters.
type Interface struct {
	Index       uint32
	Name        string // friendly name (e.g. "Ethernet", "Wi-Fi")
	Description string
	Media       MediaType
	Up          bool
	LinkMbps    uint64 // negotiated transmit link speed in Mbit/s
	MTU         uint32
	InErrors    uint64
	OutErrors   uint64
	InDiscards  uint64
	OutDiscards uint64
	InOctets    uint64
	OutOctets   uint64
}

// WiFi holds wireless link quality for the active Wi-Fi interface.
type WiFi struct {
	SSID          string
	RSSI          int    // dBm (derived from signal quality)
	SignalQuality uint32 // 0..100 as reported by Windows
	RxMbps        float64
	TxMbps        float64
}

// Info is a snapshot of local network topology and adapter health.
type Info struct {
	GatewayIP  net.IP
	Active     *Interface // egress interface for the default route (may be nil)
	Interfaces []Interface
	WiFi       *WiFi // non-nil only when the active interface is Wi-Fi and connected
}

// OnWiFi reports whether the active path is wireless.
func (i *Info) OnWiFi() bool {
	return i != nil && i.Active != nil && i.Active.Media == MediaWireless
}

// Collect gathers the current network topology. On non-Windows platforms it
// returns best-effort data and never errors fatally.
func Collect() (*Info, error) { return collect() }
