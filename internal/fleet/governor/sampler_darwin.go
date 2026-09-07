//go:build darwin

package governor

// Purpose: darwin's collectMetrics implementation (R-14.44: darwin metric
//
//	sources decided). Memory and swap come from golang.org/x/sys/unix
//	sysctls; CPU is a load-average / core-count approximation.
//
// Inputs: none (reads live host state via sysctl(3)).
// Outputs: a ResourceSnapshot with CPUFraction, MemUsedBytes,
//
//	MemTotalBytes, SwapUsedBytes, and SwapTotalBytes populated;
//	Goroutines and SampledAt are left zero here and filled in by
//	Sampler.tick (sampler.go) from the injected Clock, never from this
//	collector.
//
// Constraints: no CGO, no Mach traps (R-14.44 explicitly forbids
//
//	undocumented Mach traps for precise CPU%; this collector's
//	load-average approximation is the documented deferral). CONTRACT
//	DEVIATION: the ticket's "How" section names unix.Getloadavg as the
//	load source, but golang.org/x/sys/unix v0.47.0 (this module's pinned
//	version) has no such function on darwin — verified by symbol search
//	across the vendored source. The real equivalent is the "vm.loadavg"
//	sysctl (struct loadavg: three fixed-point uint32 loads scaled by a
//	trailing int64 fscale), read here via unix.SysctlRaw and decoded by
//	parseDarwinLoadavg. Memory-used is likewise not a single sysctl on
//	darwin without a Mach host_statistics64 call, which R-14.44 forbids;
//	this collector approximates it as MemTotalBytes minus free pages
//	(vm.page_free_count * hw.pagesize), which — like the CPU fraction —
//	does not distinguish cached/purgeable pages from truly free ones and
//	is documented as approximate for the same reason.
//
// SPORT: internal/fleet/governor.Sampler (ADD, per T-1 sport_updates).

import (
	goruntime "runtime"

	"golang.org/x/sys/unix"

	"github.com/acamarata/cascade/pkg/cascade"
)

// darwinLoadavgLen is sizeof(struct loadavg) on a 64-bit darwin host:
// three uint32 fixed-point loads (12 bytes), 4 bytes of alignment padding,
// then an 8-byte long fscale.
const darwinLoadavgLen = 24

// collectMetrics implements this package's platform seam for darwin. See
// the file-level CONTRACT DEVIATION note above for why it differs from
// the contract's literal unix.Getloadavg wording.
func collectMetrics() (ResourceSnapshot, error) {
	memTotal, memUsed, err := darwinMemoryBytes()
	if err != nil {
		return ResourceSnapshot{}, err
	}

	swapRaw, err := unix.SysctlRaw("vm.swapusage")
	if err != nil {
		return ResourceSnapshot{}, cascade.Wrap(cascade.KindUnavailable, err, "governor: sysctl vm.swapusage")
	}
	swapTotal, swapUsed, err := parseDarwinSwapusage(swapRaw)
	if err != nil {
		return ResourceSnapshot{}, err
	}

	loadRaw, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return ResourceSnapshot{}, cascade.Wrap(cascade.KindUnavailable, err, "governor: sysctl vm.loadavg")
	}
	load1, err := parseDarwinLoadavg(loadRaw)
	if err != nil {
		return ResourceSnapshot{}, err
	}
	cpu := load1 / float64(goruntime.NumCPU())
	if cpu > 1 {
		cpu = 1
	}
	if cpu < 0 {
		cpu = 0
	}

	return ResourceSnapshot{
		CPUFraction:    cpu,
		MemUsedBytes:   memUsed,
		MemTotalBytes:  memTotal,
		SwapUsedBytes:  swapUsed,
		SwapTotalBytes: swapTotal,
	}, nil
}

// darwinMemoryBytes reads total physical memory and approximates used
// memory as total minus free pages (vm.page_free_count * hw.pagesize) —
// see the file-level CONTRACT DEVIATION note for why this is an
// approximation rather than a precise accounting.
func darwinMemoryBytes() (total, used uint64, err error) {
	total, err = unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, 0, cascade.Wrap(cascade.KindUnavailable, err, "governor: sysctl hw.memsize")
	}
	freePages, err := unix.SysctlUint32("vm.page_free_count")
	if err != nil {
		return 0, 0, cascade.Wrap(cascade.KindUnavailable, err, "governor: sysctl vm.page_free_count")
	}
	pageSize, err := unix.SysctlUint32("hw.pagesize")
	if err != nil {
		return 0, 0, cascade.Wrap(cascade.KindUnavailable, err, "governor: sysctl hw.pagesize")
	}
	freeBytes := uint64(freePages) * uint64(pageSize)
	if total > freeBytes {
		used = total - freeBytes
	}
	return total, used, nil
}

// parseDarwinSwapusage decodes struct xsw_usage's leading three uint64
// fields (xsu_total, xsu_avail, xsu_used, all little-endian on darwin's
// supported architectures) from raw sysctl bytes.
func parseDarwinSwapusage(raw []byte) (total, used uint64, err error) {
	if len(raw) < 24 {
		return 0, 0, cascade.New(cascade.KindIntegrity, "governor: vm.swapusage sysctl payload too short")
	}
	total = leUint64(raw[0:8])
	used = leUint64(raw[16:24])
	return total, used, nil
}

// parseDarwinLoadavg decodes struct loadavg's three fixed-point uint32
// loads and trailing fscale, returning the 1-minute load average as a
// plain float64 (ldavg[0] / fscale).
func parseDarwinLoadavg(raw []byte) (float64, error) {
	if len(raw) < darwinLoadavgLen {
		return 0, cascade.New(cascade.KindIntegrity, "governor: vm.loadavg sysctl payload too short")
	}
	ld0 := leUint32(raw[0:4])
	fscale := leUint32(raw[16:20])
	if fscale == 0 {
		return 0, cascade.New(cascade.KindIntegrity, "governor: vm.loadavg fscale is zero")
	}
	return float64(ld0) / float64(fscale), nil
}

func leUint32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func leUint64(b []byte) uint64 {
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}
