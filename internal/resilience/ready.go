package resilience

import (
	"context"
	"time"
)

// WaitUntilReady loops every cfg.RetryInterval, calling discoverFn with
// WithinTimeout as the statFn. It returns nil only when all required volumes
// are mounted and have at least cfg.FreeSpaceFloor free bytes. If any volume
// is missing or full, it waits without side effects and retries.
func WaitUntilReady(ctx context.Context, cfg DiskConfig, discoverFn func(DiskConfig, func(string, time.Duration) (uint64, error)) ([]VolumeState, error)) error {
	for {
		if volumesReady(ctx, cfg, discoverFn) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cfg.RetryInterval):
		}
	}
}

// volumesReady runs one discovery pass and reports whether every volume is
// mounted with at least cfg.FreeSpaceFloor free bytes.
func volumesReady(ctx context.Context, cfg DiskConfig, discoverFn func(DiskConfig, func(string, time.Duration) (uint64, error)) ([]VolumeState, error)) bool {
	states, err := discoverFn(cfg, func(path string, timeout time.Duration) (uint64, error) {
		return WithinTimeout(ctx, path, timeout)
	})
	if err != nil {
		return false
	}
	for _, st := range states {
		if !st.Mounted || st.FreeBytes < cfg.FreeSpaceFloor {
			return false
		}
	}
	return true
}
