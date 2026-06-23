//go:build windows

package netinfo

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modWlanapi             = windows.NewLazySystemDLL("wlanapi.dll")
	procWlanOpenHandle     = modWlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle    = modWlanapi.NewProc("WlanCloseHandle")
	procWlanEnumInterfaces = modWlanapi.NewProc("WlanEnumInterfaces")
	procWlanQueryInterface = modWlanapi.NewProc("WlanQueryInterface")
	procWlanFreeMemory     = modWlanapi.NewProc("WlanFreeMemory")
)

const (
	wlanInterfaceStateConnected   = 1
	wlanIntfOpcodeCurrentConn     = 7
	wlanClientVersionVistaOrLater = 2
)

type wlanInterfaceInfo struct {
	Guid        windows.GUID
	Description [256]uint16
	State       uint32
}

type wlanInterfaceInfoList struct {
	NumberOfItems uint32
	Index         uint32
	InterfaceInfo [1]wlanInterfaceInfo
}

type dot11SSID struct {
	Length uint32
	SSID   [32]byte
}

type wlanAssocAttributes struct {
	Dot11SSID     dot11SSID
	Dot11BssType  uint32
	Dot11Bssid    [6]byte
	Dot11PhyType  uint32
	Dot11PhyIndex uint32
	SignalQuality uint32 // 0..100
	RxRate        uint32 // Kbps
	TxRate        uint32 // Kbps
}

// wlanConnectionAttributes mirrors WLAN_CONNECTION_ATTRIBUTES up to the
// association block (the trailing security attributes are not needed).
type wlanConnectionAttributes struct {
	State       uint32
	Mode        uint32
	ProfileName [256]uint16
	Assoc       wlanAssocAttributes
}

// collectWiFi returns the RSSI/link info for the first connected Wi-Fi
// interface, or nil if none / on any error (degrade gracefully).
func collectWiFi() *WiFi {
	if err := procWlanOpenHandle.Find(); err != nil {
		return nil
	}
	var negotiated uint32
	var handle windows.Handle
	if r, _, _ := procWlanOpenHandle.Call(
		uintptr(wlanClientVersionVistaOrLater), 0,
		uintptr(unsafe.Pointer(&negotiated)), uintptr(unsafe.Pointer(&handle)),
	); r != 0 {
		return nil
	}
	defer procWlanCloseHandle.Call(uintptr(handle), 0)

	var list *wlanInterfaceInfoList
	if r, _, _ := procWlanEnumInterfaces.Call(
		uintptr(handle), 0, uintptr(unsafe.Pointer(&list)),
	); r != 0 || list == nil {
		return nil
	}
	defer procWlanFreeMemory.Call(uintptr(unsafe.Pointer(list)))

	n := list.NumberOfItems
	first := unsafe.Pointer(&list.InterfaceInfo[0])
	stride := unsafe.Sizeof(wlanInterfaceInfo{})
	for i := uint32(0); i < n; i++ {
		ifi := (*wlanInterfaceInfo)(unsafe.Add(first, uintptr(i)*stride))
		if ifi.State != wlanInterfaceStateConnected {
			continue
		}
		if w := queryConnection(handle, &ifi.Guid); w != nil {
			return w
		}
	}
	return nil
}

func queryConnection(handle windows.Handle, guid *windows.GUID) *WiFi {
	var dataSize uint32
	var pData unsafe.Pointer
	if r, _, _ := procWlanQueryInterface.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(guid)),
		uintptr(wlanIntfOpcodeCurrentConn),
		0,
		uintptr(unsafe.Pointer(&dataSize)),
		uintptr(unsafe.Pointer(&pData)),
		0,
	); r != 0 || pData == nil {
		return nil
	}
	defer procWlanFreeMemory.Call(uintptr(pData))

	if dataSize < uint32(unsafe.Sizeof(wlanConnectionAttributes{})) {
		return nil
	}
	attr := (*wlanConnectionAttributes)(pData)
	a := attr.Assoc

	ssidLen := a.Dot11SSID.Length
	if ssidLen > 32 {
		ssidLen = 32
	}
	q := a.SignalQuality
	if q > 100 {
		q = 100
	}
	return &WiFi{
		SSID:          string(a.Dot11SSID.SSID[:ssidLen]),
		SignalQuality: q,
		RSSI:          int(q)/2 - 100, // Windows quality 0..100 -> approx dBm
		RxMbps:        float64(a.RxRate) / 1000.0,
		TxMbps:        float64(a.TxRate) / 1000.0,
	}
}
