//go:build windows

package resilience

import (
	"context"

	"golang.org/x/sys/windows"
)

// defaultStat calls GetDiskFreeSpaceEx and returns the free bytes available
// to the calling user.
func defaultStat(_ context.Context, path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var avail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &totalFree); err != nil {
		return 0, err
	}
	return avail, nil
}
