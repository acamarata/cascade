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
//	parseDarwinLoadavg. Used memory follows the kernel's own pressure
//	figure: used% = 100 - kern.memorystatus_level, so MemTotalBytes =
//	hw.memsize and MemUsedBytes = hw.memsize * (100 - level) / 100.
//	Counting free, speculative and purgeable pages instead reads inactive
//	and file-backed pages as used and puts a healthy host near 99%. A
//	failed sysctl, a value that is not 4 or 8 bytes wide, a level above
//	100 or a zero hw.memsize is a KindUnavailable error, never a guess.
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

// darwinMemoryBytes reads live memory sysctls through darwinMemoryFrom.
func darwinMemoryBytes() (total, used uint64, err error) {
	return darwinMemoryFrom(darwinSysctlCount)
}

// darwinSysctlCount reads an integer sysctl through darwinSysctlValue.
// A failed read is KindUnavailable: the sample is refused.
func darwinSysctlCount(name string) (uint64, error) {
	raw, err := unix.SysctlRaw(name)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "governor: sysctl "+name)
	}
	return darwinSysctlValue(name, raw)
}

// darwinSysctlValue decodes a little-endian integer sysctl value of 4 or
// 8 bytes (widths differ by counter and OS release). Any other width is
// a failed read (KindUnavailable), never a partial or zero value.
func darwinSysctlValue(name string, raw []byte) (uint64, error) {
	switch len(raw) {
	case 4:
		return uint64(leUint32(raw)), nil
	case 8:
		return leUint64(raw), nil
	}
	return 0, cascade.Newf(cascade.KindUnavailable, "governor: sysctl %s has %d-byte value", name, len(raw))
}

// darwinMemoryFrom derives memory use from the kernel's pressure level
// over an injected sysctl reader: used = hw.memsize * (100 -
// kern.memorystatus_level) / 100. A failed read, a zero hw.memsize or a
// level above 100 is a KindUnavailable error and the caller publishes
// nothing.
func darwinMemoryFrom(sysctl func(string) (uint64, error)) (total, used uint64, err error) {
	total, err = sysctl("hw.memsize")
	if err != nil || total == 0 {
		return 0, 0, cascade.Wrapf(cascade.KindUnavailable, err, "governor: sysctl hw.memsize unavailable (total %d)", total)
	}
	level, err := sysctl("kern.memorystatus_level")
	if err != nil {
		return 0, 0, cascade.Wrap(cascade.KindUnavailable, err, "governor: sysctl kern.memorystatus_level")
	}
	if level > 100 {
		return 0, 0, cascade.Newf(cascade.KindUnavailable, "governor: kern.memorystatus_level %d is above 100", level)
	}
	return total, total * (100 - level) / 100, nil
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
