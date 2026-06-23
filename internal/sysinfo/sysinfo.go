// Package sysinfo samples local host resource usage that can masquerade as a
// network problem: CPU saturation, memory pressure, and total NIC throughput
// (a saturating upload/download from this very machine inflates latency and
// looks like ISP trouble). It also surfaces the top CPU-consuming processes so
// the classifier can name a local culprit. Built on gopsutil; cross-platform.
package sysinfo

import (
	"context"
	"sort"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// Stats is a point-in-time sample of host resource usage.
type Stats struct {
	CPUPercent float64 // 0..100 across all cores
	MemPercent float64 // 0..100 used
	InMbps     float64 // aggregate received throughput since last sample
	OutMbps    float64 // aggregate sent throughput since last sample
}

// ProcCPU identifies a process and its resource usage.
type ProcCPU struct {
	PID   int32
	Name  string
	CPU   float64 // percent
	MemMB float64 // resident memory in MB
}

// Collector holds the state needed to compute throughput rates between samples.
// It is not safe for concurrent use.
type Collector struct {
	lastRecv uint64
	lastSent uint64
	lastTime time.Time
	primed   bool
}

// NewCollector returns a ready-to-use Collector.
func NewCollector() *Collector { return &Collector{} }

// Sample captures CPU%, memory%, and NIC throughput since the previous call.
// The first call primes the throughput baseline and reports 0 Mbps.
func (c *Collector) Sample(ctx context.Context) (Stats, error) {
	var st Stats

	if cpus, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(cpus) > 0 {
		st.CPUPercent = cpus[0]
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		st.MemPercent = vm.UsedPercent
	}

	now := time.Now()
	if counters, err := net.IOCountersWithContext(ctx, false); err == nil && len(counters) > 0 {
		recv, sent := counters[0].BytesRecv, counters[0].BytesSent
		if c.primed {
			dt := now.Sub(c.lastTime).Seconds()
			// Guard against counter resets/wraparound (e.g. adapter reconnect),
			// which would otherwise underflow the unsigned subtraction.
			if dt > 0 && recv >= c.lastRecv && sent >= c.lastSent {
				st.InMbps = bytesToMbps(recv-c.lastRecv, dt)
				st.OutMbps = bytesToMbps(sent-c.lastSent, dt)
			}
		}
		c.lastRecv, c.lastSent, c.lastTime, c.primed = recv, sent, now, true
	}

	return st, nil
}

func bytesToMbps(deltaBytes uint64, seconds float64) float64 {
	if seconds <= 0 {
		return 0
	}
	return float64(deltaBytes) * 8 / 1e6 / seconds
}

// TopCPUProcesses returns up to n processes sorted by descending CPU usage.
// This is comparatively expensive (it enumerates all processes), so it is meant
// to be called on demand — e.g. only when CPU pressure is already detected.
func TopCPUProcesses(ctx context.Context, n int) ([]ProcCPU, error) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ProcCPU, 0, len(procs))
	for _, p := range procs {
		cp, err := p.CPUPercent()
		if err != nil {
			continue
		}
		name, _ := p.Name()
		var memMB float64
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			memMB = float64(mi.RSS) / (1024 * 1024)
		}
		out = append(out, ProcCPU{PID: p.Pid, Name: name, CPU: cp, MemMB: memMB})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPU > out[j].CPU })
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}
