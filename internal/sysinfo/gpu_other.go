//go:build !windows

package sysinfo

// gpuQuery is a no-op on non-Windows platforms; GPU utilization reporting is
// currently Windows-only (via PDH performance counters).
type gpuQuery struct{}

func newGPUQuery() *gpuQuery { return nil }

func (g *gpuQuery) sample() (float64, bool) { return 0, false }
