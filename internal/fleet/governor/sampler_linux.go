//go:build linux

package governor

// Purpose: linux's collectMetrics implementation. CPU comes from a delta
//
//	across /proc/stat's aggregate "cpu " line between consecutive ticks;
//	memory and swap come from /proc/meminfo. Both are pure-Go file reads
//	via the untagged parseProcStat/parseProcMeminfo helpers in sampler.go
//	— kept there specifically so they compile and fuzz on every host
//	platform, not only linux (see sampler.go's file-level constraints
//	note).
//
// Inputs: none (reads /proc/stat and /proc/meminfo).
// Outputs: a ResourceSnapshot with CPUFraction, MemUsedBytes,
//
//	MemTotalBytes, SwapUsedBytes, and SwapTotalBytes populated;
//	Goroutines and SampledAt are filled in by Sampler.tick, not here.
//
// Constraints: no CGO, no cgroups accounting for P1 scope (contract's
//
//	explicit "no cgroups for P1 scope"). CPU-delta state is process-wide
//	package state (procStatMu-guarded), not per-Sampler-instance, because
//	collectMetrics has no receiver — matching the contract's stated
//	signature exactly; a process runs at most one daemon governor
//	sampler, so this is not a practical multi-instance concern.
//
// SPORT: internal/fleet/governor.Sampler (ADD, per T-1 sport_updates).

import (
	"os"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	procStatPath    = "/proc/stat"
	procMeminfoPath = "/proc/meminfo"
)

// procStatState holds the previous /proc/stat sample so collectMetrics can
// compute a CPU-utilization delta across ticks, per the contract's "CPU
// via /proc/stat delta across ticks" instruction.
var (
	procStatMu    sync.Mutex
	procStatState struct {
		idle, total uint64
		valid       bool
	}
)

// collectMetrics implements this package's platform seam for linux.
func collectMetrics() (ResourceSnapshot, error) {
	statData, err := os.ReadFile(procStatPath)
	if err != nil {
		return ResourceSnapshot{}, cascade.Wrap(cascade.KindUnavailable, err, "governor: read /proc/stat")
	}
	idle, total, err := parseProcStat(statData)
	if err != nil {
		return ResourceSnapshot{}, err
	}

	memData, err := os.ReadFile(procMeminfoPath)
	if err != nil {
		return ResourceSnapshot{}, cascade.Wrap(cascade.KindUnavailable, err, "governor: read /proc/meminfo")
	}
	memTotal, memAvail, swapTotal, swapFree := parseProcMeminfo(memData)

	memUsed := uint64(0)
	if memTotal > memAvail {
		memUsed = memTotal - memAvail
	}
	swapUsed := uint64(0)
	if swapTotal > swapFree {
		swapUsed = swapTotal - swapFree
	}

	return ResourceSnapshot{
		CPUFraction:    cpuFractionFromDelta(idle, total),
		MemUsedBytes:   memUsed,
		MemTotalBytes:  memTotal,
		SwapUsedBytes:  swapUsed,
		SwapTotalBytes: swapTotal,
	}, nil
}

// cpuFractionFromDelta computes utilization as 1 minus the idle share of
// the jiffy delta since the previous call. The first call after process
// start (or after a counter regression, which should never happen but is
// treated defensively rather than trusted) has no baseline and reports 0.
func cpuFractionFromDelta(idle, total uint64) float64 {
	procStatMu.Lock()
	defer procStatMu.Unlock()

	prevIdle, prevTotal, valid := procStatState.idle, procStatState.total, procStatState.valid
	procStatState.idle, procStatState.total, procStatState.valid = idle, total, true

	if !valid || total <= prevTotal {
		return 0
	}
	totalDelta := total - prevTotal
	idleDelta := idle - prevIdle
	if idleDelta > totalDelta {
		return 0
	}
	return float64(totalDelta-idleDelta) / float64(totalDelta)
}
