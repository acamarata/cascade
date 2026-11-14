//go:build darwin

package testkit

import (
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// loadAverageSysctl reads the 1-minute load average from the vm.loadavg
// sysctl (struct loadavg: three uint32 averages, 4 bytes of padding, then the
// int64 fixed-point scale); any failure reports 0.
func loadAverageSysctl() float64 {
	b, err := unix.SysctlRaw("vm.loadavg")
	if err != nil || len(b) < 24 {
		return 0
	}
	scale := binary.NativeEndian.Uint64(b[16:24])
	if scale == 0 {
		return 0
	}
	return float64(binary.NativeEndian.Uint32(b[0:4])) / float64(scale)
}
