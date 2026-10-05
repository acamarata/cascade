package daemon

// Purpose (this file): the source pull behind status.widget and its refresh
// loop: re-read providers.db and the node records into the Compositor, and
// say whether a read failed.
//
// Inputs: StatusWidgetDeps' provider and node sources.
// Outputs: a FleetSnapshot and a stale flag.
// Constraints: a read that fails AFTER an earlier success leaves the
// Compositor's last slots untouched (Compositor.Update* return before they
// write), so every row keeps its own updated_at and ages; the snapshot is
// served with stale set, and the emit path publishes nothing for it. A
// source that has NEVER been read successfully is an error instead: serving
// no rows then would read as "no providers" rather than "not read yet". The
// Compositor's TTL turns a persistently failing source into unknown rows
// (compositor_build.go's expiry), never into last-good values.
//
// SPORT: daemon.status_widget (CHANGE, P1-WID-08).

import (
	"context"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/fleet/capacity"
)

// sourceReads records which sources have been read successfully at least
// once.
type sourceReads struct{ providers, nodes atomic.Bool }

// capacitySnapshot pulls the provider and node sources fresh (nil-safe) and
// returns the resulting FleetSnapshot. stale is true when a source read
// failed and its last slots were kept; err is non-nil only when a source
// failed before it had ever been read.
//
// MUTEX NOTE: Compositor.Update* take the Compositor's own lock and call no
// callback of ours (the old onChange trigger is gone), so this is safe to
// call from the RPC handler, the refresh loop and the attention hook alike.
func (d *StatusWidgetDeps) capacitySnapshot(ctx context.Context) (snap capacity.FleetSnapshot, stale bool, err error) {
	if d.providerSrc != nil {
		failed, err := pullSource(&d.reads.providers, func() error { return d.comp.UpdateProviders(ctx, d.providerSrc) })
		if err != nil {
			return capacity.FleetSnapshot{}, false, err
		}
		stale = stale || failed
	}
	if d.nodeSrc != nil {
		failed, err := pullSource(&d.reads.nodes, func() error { return d.comp.UpdateNodes(ctx, d.nodeSrc) })
		if err != nil {
			return capacity.FleetSnapshot{}, false, err
		}
		stale = stale || failed
	}
	return d.comp.Snapshot(), stale, nil
}

// pullSource runs one source update. It returns failed=true when the update
// failed after a success (last slots kept) and the error itself when the
// source has never succeeded.
func pullSource(everRead *atomic.Bool, update func() error) (failed bool, err error) {
	if uerr := update(); uerr != nil {
		if !everRead.Load() {
			return false, uerr
		}
		return true, nil
	}
	everRead.Store(true)
	return false, nil
}
