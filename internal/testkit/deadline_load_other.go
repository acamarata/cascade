//go:build !darwin

package testkit

// loadAverageSysctl reports no load where neither /proc/loadavg nor the
// darwin sysctl exists, so deadlines scale only by CASCADE_TEST_DEADLINE_SCALE.
func loadAverageSysctl() float64 { return 0 }
