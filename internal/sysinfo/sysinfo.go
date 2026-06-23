// Package sysinfo samples local host resource usage that can masquerade as a
// network problem: CPU saturation, memory pressure, and total NIC throughput
// (a saturating upload/download from this very machine inflates latency and
// looks like ISP trouble). It also surfaces the top CPU-consuming processes so
// the classifier can name a local culprit. Built on gopsutil; cross-platform.
package sysinfo

import (
	"context"
	"runtime"
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
	MemUsedGB  float64 // used physical memory (GiB)
	MemTotalGB float64 // total physical memory (GiB)
	GPUPercent float64 // 0..100 busiest GPU engine; -1 when unavailable
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
	gpu      *gpuQuery // platform GPU sampler; nil when unavailable
}

// NewCollector returns a ready-to-use Collector.
func NewCollector() *Collector { return &Collector{gpu: newGPUQuery()} }

// Sample captures CPU%, memory%, and NIC throughput since the previous call.
// The first call primes the throughput baseline and reports 0 Mbps.
func (c *Collector) Sample(ctx context.Context) (Stats, error) {
	var st Stats
	st.GPUPercent = -1 // unknown unless a sampler provides it

	if cpus, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(cpus) > 0 {
		st.CPUPercent = cpus[0]
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		st.MemPercent = vm.UsedPercent
		st.MemUsedGB = float64(vm.Used) / (1 << 30)
		st.MemTotalGB = float64(vm.Total) / (1 << 30)
	}
	if c.gpu != nil {
		if v, ok := c.gpu.sample(); ok {
			st.GPUPercent = v
		}
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
//
// CPU is normalized to a share of TOTAL system capacity (0..100%), matching
// Task Manager: gopsutil reports process CPU per-core (so a process spanning
// several cores can read 600%+), which we divide by the logical core count.
func TopCPUProcesses(ctx context.Context, n int) ([]ProcCPU, error) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	cores := float64(runtime.NumCPU())
	if cores < 1 {
		cores = 1
	}
	out := make([]ProcCPU, 0, len(procs))
	for _, p := range procs {
		cp, err := p.CPUPercent()
		if err != nil {
			continue
		}
		cp /= cores
		if cp > 100 {
			cp = 100
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
