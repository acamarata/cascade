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
// Constraints: no CGO, no cgroups accounting. A missing reading is never
//
//	reported as a measured zero: the first read (no CPU baseline) and a
//	jiffy-counter regression return ErrCPUBaselinePending, and a meminfo
//	without a parseable MemTotal or MemAvailable, or with MemAvailable
//	above MemTotal, returns KindUnavailable, so tick()
//	publishes nothing for that period. CPU-delta state lives in the
//	process-wide hostProcCollector because collectMetrics has no receiver.
//
// SPORT: internal/fleet/governor.Sampler (ADD, per T-1 sport_updates).

import (
	"math"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	procStatPath    = "/proc/stat"
	procMeminfoPath = "/proc/meminfo"
)

// ErrCPUBaselinePending is returned by the linux collector when it has no
// earlier /proc/stat sample to take a CPU delta against (the first read, or
// the read after a counter regression). Nothing is published for that
// period; the next read measures against the baseline just recorded.
var ErrCPUBaselinePending = cascade.New(cascade.KindUnavailable, "governor: cpu baseline pending; no utilization measured yet")

// procCollector reads /proc through read and keeps the previous
// /proc/stat counters for the CPU delta.
type procCollector struct {
	read func(path string) ([]byte, error)

	mu                  sync.Mutex
	prevIdle, prevTotal uint64
	valid               bool
}

// hostProcCollector is the live collector collectMetrics delegates to.
var hostProcCollector = &procCollector{read: os.ReadFile}

// collectMetrics implements this package's platform seam for linux.
func collectMetrics() (ResourceSnapshot, error) {
	return hostProcCollector.collect()
}

// collect reads /proc/stat and /proc/meminfo once. It returns an error,
// never a zero-filled snapshot, for any reading it could not measure.
func (c *procCollector) collect() (ResourceSnapshot, error) {
	statData, err := c.read(procStatPath)
	if err != nil {
		return ResourceSnapshot{}, cascade.Wrap(cascade.KindUnavailable, err, "governor: read /proc/stat")
	}
	idle, total, err := parseProcStat(statData)
	if err != nil {
		return ResourceSnapshot{}, err
	}
	cpu, err := c.cpuFractionFromDelta(idle, total)
	if err != nil {
		return ResourceSnapshot{}, err
	}
	memData, err := c.read(procMeminfoPath)
	if err != nil {
		return ResourceSnapshot{}, cascade.Wrap(cascade.KindUnavailable, err, "governor: read /proc/meminfo")
	}
	memTotal, memAvail, err := meminfoMemory(memData)
	if err != nil {
		return ResourceSnapshot{}, err
	}
	_, _, swapTotal, swapFree := parseProcMeminfo(memData)
	snap := ResourceSnapshot{CPUFraction: cpu, MemTotalBytes: memTotal, MemUsedBytes: memTotal - memAvail, SwapTotalBytes: swapTotal}
	if swapTotal > swapFree {
		snap.SwapUsedBytes = swapTotal - swapFree
	}
	return snap, nil
}

// cpuFractionFromDelta computes utilization as 1 minus the idle share of
// the jiffy delta since the previous call, and records (idle, total) as
// the next baseline. With no baseline, or when either counter went
// backwards, it returns ErrCPUBaselinePending instead of a 0 reading.
func (c *procCollector) cpuFractionFromDelta(idle, total uint64) (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prevIdle, prevTotal, valid := c.prevIdle, c.prevTotal, c.valid
	c.prevIdle, c.prevTotal, c.valid = idle, total, true
	if !valid {
		return 0, cascade.Wrapf(cascade.KindUnavailable, ErrCPUBaselinePending, "first /proc/stat read")
	}
	if total <= prevTotal || idle < prevIdle || idle-prevIdle > total-prevTotal {
		return 0, cascade.Wrapf(cascade.KindUnavailable, ErrCPUBaselinePending,
			"/proc/stat counters regressed (total %d -> %d)", prevTotal, total)
	}
	totalDelta := total - prevTotal
	return float64(totalDelta-(idle-prevIdle)) / float64(totalDelta), nil
}

// meminfoMemory reads MemTotal and MemAvailable in bytes. A missing,
// empty or unparseable value, a zero MemTotal, or MemAvailable above
// MemTotal is KindUnavailable: memory use is unmeasured, never read as
// idle or as fully used.
func meminfoMemory(data []byte) (total, avail uint64, err error) {
	total, okTotal := meminfoValue(data, "MemTotal")
	avail, okAvail := meminfoValue(data, "MemAvailable")
	if !okTotal || !okAvail || total == 0 {
		return 0, 0, cascade.New(cascade.KindUnavailable,
			"governor: /proc/meminfo lacks a parseable MemTotal or MemAvailable; memory use unmeasured")
	}
	if avail > total {
		return 0, 0, cascade.Newf(cascade.KindUnavailable,
			"governor: /proc/meminfo MemAvailable %d exceeds MemTotal %d; memory use unmeasured", avail, total)
	}
	return total, avail, nil
}

// meminfoValue returns the first key line's value in bytes (the file
// reports kB); ok is false when the line is missing, empty or unparseable.
func meminfoValue(data []byte, key string) (uint64, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != key+":" {
			continue
		}
		if len(fields) < 2 {
			return 0, false
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || n > math.MaxUint64/1024 {
			return 0, false
		}
		return n * 1024, true
	}
	return 0, false
}
