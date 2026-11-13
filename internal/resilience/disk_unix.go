//go:build unix

package resilience

import (
	"context"
	"syscall"
)

// defaultStat performs a syscall.Statfs and returns the available free bytes.
func defaultStat(_ context.Context, path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
