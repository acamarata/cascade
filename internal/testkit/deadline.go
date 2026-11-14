package testkit

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scaleEnv names the variable that multiplies every test deadline.
const scaleEnv = "CASCADE_TEST_DEADLINE_SCALE"

// maxLoadScale caps the automatic load-based multiplier.
const maxLoadScale = 10.0

// Deadline returns base stretched for the machine it runs on: base times
// CASCADE_TEST_DEADLINE_SCALE (default 1; values that do not parse or are
// below 1 count as 1), then times the 1-minute load average over the CPU
// count when that ratio exceeds 1, capped at 10x. A hung child still fails,
// only later.
func Deadline(t testing.TB, base time.Duration) time.Duration {
	t.Helper()
	return scaleDeadline(base, os.Getenv(scaleEnv), loadAverage1(), runtime.NumCPU())
}

// scaleDeadline is Deadline with its inputs explicit.
func scaleDeadline(base time.Duration, scale string, load float64, cpus int) time.Duration {
	factor := 1.0
	if v, err := strconv.ParseFloat(strings.TrimSpace(scale), 64); err == nil && v > 1 {
		factor = v
	}
	if cpus > 0 && load > float64(cpus) {
		factor *= load / float64(cpus)
	}
	if factor > maxLoadScale {
		factor = maxLoadScale
	}
	return time.Duration(float64(base) * factor)
}

// loadAverage1 reads the 1-minute load average from /proc/loadavg; platforms
// without it report 0 (no load scaling).
func loadAverage1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	return parseLoadavg(b, err)
}

// parseLoadavg reads the 1-minute load from /proc/loadavg content, and falls
// back to the platform sysctl when the file could not be read.
func parseLoadavg(b []byte, err error) float64 {
	if err != nil {
		return loadAverageSysctl()
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}
