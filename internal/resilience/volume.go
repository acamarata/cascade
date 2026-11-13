package resilience

import (
	"sync"
	"time"
)

// knownVolumes tracks volumes that have been seen mounted at least once.
// Once a volume becomes known it remains required permanently.
var (
	knownMu      sync.Mutex
	knownVolumes = make(map[string]bool)
)

// DiscoverVolumes checks each path in cfg.RequiredVolumes and cfg.DataHome.
// A volume is Mounted if statFn succeeds without ErrStatTimedOut. The first
// time a volume is seen mounted its Known field becomes true and it becomes
// permanently required. If a previously known volume is now unmounted it is
// treated as an empty folder on the boot disk (ErrVolumeMissing). Config
// overrides can force Known=true via knownVolumes before calling this func.
func DiscoverVolumes(cfg DiskConfig, statFn func(string, time.Duration) (uint64, error)) ([]VolumeState, error) {
	paths := make([]string, 0, len(cfg.RequiredVolumes)+1)
	paths = append(paths, cfg.RequiredVolumes...)
	if cfg.DataHome != "" {
		paths = append(paths, cfg.DataHome)
	}

	knownMu.Lock()
	defer knownMu.Unlock()

	seen := make(map[string]bool)
	var states []VolumeState
	var firstErr error

	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true

		vs := VolumeState{Path: p}

		if knownVolumes[p] {
			vs.Known = true
		}

		free, err := statFn(p, cfg.StatTimeout)
		if err == nil {
			vs.Mounted = true
			vs.FreeBytes = int64(free)
			vs.Known = true
			knownVolumes[p] = true
		} else {
			vs.Mounted = false
			if vs.Known {
				if firstErr == nil {
					firstErr = ErrVolumeMissing
				}
			}
		}

		states = append(states, vs)
	}

	return states, firstErr
}
