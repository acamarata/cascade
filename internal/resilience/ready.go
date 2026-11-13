package resilience

import (
	"context"
	"fmt"
	"time"
)

// discoverFunc is the shape of DiscoverVolumes, injected so tests can drive the loop.
type discoverFunc func(DiskConfig, func(string, time.Duration) (uint64, error)) ([]VolumeState, error)

// WaitUntilReady loops every cfg.RetryInterval, calling discoverFn with
// WithinTimeout as the statFn. It returns nil only when all required volumes
// are mounted and have at least cfg.FreeSpaceFloor free bytes. A missing volume
// (ErrVolumeMissing) or one under the floor (ErrVolumeFull) means wait: it
// starts nothing, writes nothing and retries. If ctx ends first, the returned
// error wraps ctx.Err() and the last wait reason, so the caller can name it.
func WaitUntilReady(ctx context.Context, cfg DiskConfig, discoverFn discoverFunc) error {
	for {
		reason := waitReason(ctx, cfg, discoverFn)
		if reason == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (waiting: %w)", ctx.Err(), reason)
		case <-time.After(cfg.RetryInterval):
		}
	}
}

// waitReason runs one discovery pass. It returns nil when every volume is
// mounted with at least cfg.FreeSpaceFloor free bytes, otherwise an error
// wrapping ErrVolumeMissing or ErrVolumeFull that names the volume.
func waitReason(ctx context.Context, cfg DiskConfig, discoverFn discoverFunc) error {
	states, err := discoverFn(cfg, func(path string, timeout time.Duration) (uint64, error) {
		return WithinTimeout(ctx, path, timeout)
	})
	if err != nil {
		return err
	}
	for _, st := range states {
		if !st.Mounted {
			return fmt.Errorf("%w: %s", ErrVolumeMissing, st.Path)
		}
		if st.FreeBytes < cfg.FreeSpaceFloor {
			return fmt.Errorf("%w: %s", ErrVolumeFull, st.Path)
		}
	}
	return nil
}
