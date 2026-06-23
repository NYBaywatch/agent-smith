//go:build windows

package sysinfo

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// GPU utilization on Windows is read from the PDH performance counter
// "\GPU Engine(*)\Utilization Percentage" — the same source Task Manager uses.
// We aggregate by taking the busiest engine instance, which closely tracks the
// headline GPU% a user sees while gaming.

const (
	pdhFmtDouble        = 0x00000200
	pdhMoreData         = 0x800007D2
	pdhCStatusValidData = 0x00000000
	pdhCStatusNewData   = 0x00000001
)

var (
	modPdh                      = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQuery            = modPdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounter    = modPdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData     = modPdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCtrArray = modPdh.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery           = modPdh.NewProc("PdhCloseQuery")
)

type pdhFmtCountervalueDouble struct {
	CStatus     uint32
	pad         uint32
	DoubleValue float64
}

type pdhFmtCountervalueItemW struct {
	szName   *uint16
	FmtValue pdhFmtCountervalueDouble
}

type gpuQuery struct {
	query   uintptr
	counter uintptr
}

// newGPUQuery opens a PDH query for GPU engine utilization. It returns nil if
// PDH or the GPU counters are unavailable (e.g. no GPU / counters disabled).
func newGPUQuery() *gpuQuery {
	if procPdhOpenQuery.Find() != nil {
		return nil
	}
	var query uintptr
	if r, _, _ := procPdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&query))); r != 0 {
		return nil
	}
	path, err := windows.UTF16PtrFromString(`\GPU Engine(*)\Utilization Percentage`)
	if err != nil {
		procPdhCloseQuery.Call(query)
		return nil
	}
	var counter uintptr
	if r, _, _ := procPdhAddEnglishCounter.Call(query, uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&counter))); r != 0 {
		procPdhCloseQuery.Call(query)
		return nil
	}
	// Prime: rate counters need an initial collection to establish a baseline.
	procPdhCollectQueryData.Call(query)
	return &gpuQuery{query: query, counter: counter}
}

// sample returns the busiest GPU engine utilization (0..100) since the previous
// sample, or ok=false if it could not be read.
func (g *gpuQuery) sample() (float64, bool) {
	if g == nil {
		return 0, false
	}
	if r, _, _ := procPdhCollectQueryData.Call(g.query); r != 0 {
		return 0, false
	}

	var size, count uint32
	r, _, _ := procPdhGetFormattedCtrArray.Call(g.counter, pdhFmtDouble,
		uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if uint32(r) != pdhMoreData || size == 0 {
		return 0, false
	}

	buf := make([]byte, size)
	r, _, _ = procPdhGetFormattedCtrArray.Call(g.counter, pdhFmtDouble,
		uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0])))
	if r != 0 || count == 0 {
		return 0, false
	}

	items := unsafe.Slice((*pdhFmtCountervalueItemW)(unsafe.Pointer(&buf[0])), count)
	maxV := 0.0
	for i := range items {
		st := items[i].FmtValue.CStatus
		if st != pdhCStatusValidData && st != pdhCStatusNewData {
			continue
		}
		if v := items[i].FmtValue.DoubleValue; v > maxV {
			maxV = v
		}
	}
	if maxV > 100 {
		maxV = 100
	}
	return maxV, true
}
