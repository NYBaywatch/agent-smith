//go:build !windows

package netinfo

import (
	"net"
	"strings"
)

// collect is the best-effort non-Windows fallback. It enumerates interfaces via
// the standard library and does not attempt gateway/RSSI discovery (the rich
// implementation is Windows-only). It never errors fatally.
func collect() (*Info, error) {
	info := &Info{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return info, nil
	}
	for _, ni := range ifaces {
		if ni.Flags&net.FlagLoopback != 0 {
			continue
		}
		iface := Interface{
			Index: uint32(ni.Index),
			Name:  ni.Name,
			Media: guessMedia(ni.Name),
			Up:    ni.Flags&net.FlagUp != 0,
			MTU:   uint32(ni.MTU),
		}
		info.Interfaces = append(info.Interfaces, iface)
		if info.Active == nil && iface.Up {
			info.Active = &info.Interfaces[len(info.Interfaces)-1]
		}
	}
	return info, nil
}

func guessMedia(name string) MediaType {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "wl") || strings.Contains(n, "wifi") || strings.Contains(n, "wlan"):
		return MediaWireless
	case strings.HasPrefix(n, "en") || strings.HasPrefix(n, "eth"):
		return MediaWired
	default:
		return MediaOther
	}
}
