package resilience

import (
	"context"
	"errors"
	"syscall"
	"time"
)

// DiskConfig holds the configuration for the disk resilience check.
// Defaults are documented here for consumers; this type does not apply them.
type DiskConfig struct {
	// RequiredVolumes is the list of mount paths that must be present and writable.
	RequiredVolumes []string
	// DataHome is the primary directory for application data. While a required
	// volume is missing, disk checks must not write outside this directory.
	DataHome string
	// FreeSpaceFloor is the minimum acceptable free space in bytes (default 1 << 30).
	FreeSpaceFloor int64
	// RetryInterval is the duration between disk check attempts (default 60 seconds).
	RetryInterval time.Duration
	// StatTimeout is the maximum duration allowed for a filesystem stat operation.
	StatTimeout time.Duration
}

// VolumeState captures the current status of a specific volume.
type VolumeState struct {
	// Path is the mount point or directory being checked.
	Path string
	// Mounted indicates whether the volume is currently mounted or accessible.
	Mounted bool
	// FreeBytes is the number of free bytes available on the volume.
	FreeBytes int64
	// Known indicates whether the volume's state has been successfully determined.
	Known bool
}

var (
	// ErrVolumeMissing is returned when a required volume is not mounted or accessible.
	ErrVolumeMissing = errors.New("disk: required volume missing")
	// ErrVolumeFull is returned when the free space falls below the configured floor.
	ErrVolumeFull = errors.New("disk: volume full")
	// ErrStatTimedOut is returned when a filesystem stat operation exceeds the StatTimeout.
	ErrStatTimedOut = errors.New("disk: stat operation timed out")
)

// statFunc is the function used to perform a filesystem stat. It can be
// overridden in tests to simulate blocking behaviour.
var statFunc = defaultStat

// defaultStat performs a syscall.Statfs and returns the available free bytes.
func defaultStat(_ context.Context, path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

// WithinTimeout executes a filesystem stat on path inside a goroutine,
// returning free bytes if it completes before timeout. If the stat call
// stalls beyond timeout, it cancels the context and returns ErrStatTimedOut.
// This ensures a stalled network share cannot hang the disk check pass.
func WithinTimeout(ctx context.Context, path string, timeout time.Duration) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		free uint64
		err  error
	}

	ch := make(chan result, 1)
	go func() {
		free, err := statFunc(ctx, path)
		ch <- result{free: free, err: err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return 0, r.err
		}
		return r.free, nil
	case <-ctx.Done():
		return 0, ErrStatTimedOut
	}
}
