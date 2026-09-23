package plugins

// Purpose (this file): the durable-state close helpers cascadepa_bridge_wiring.go's
//   enabledBridge and BridgeRuntime.Stop use to release the SQLite handle
//   openBridgeState opened. Split out of cascadepa_bridge_wiring.go to keep it
//   under the 300-line cap (P1-E23-W5-S48-T4 producer fix, mechanical split,
//   no behaviour change).
//
// Inputs: the cascadepa.BridgeState the composition root opened, and either an
//   assembly error (closeStateOnError) or the module's own Stop func
//   (closeStateAfterStop).
//
// Outputs: closeStateOnError returns the original error unchanged, with the
//   close failure folded in as context on close failure; closeStateAfterStop
//   returns a Stop wrapper that releases the handle only after the module has
//   actually drained.
//
// Constraints: state is typed cascadepa.BridgeState (the plugin-facing seam,
//   which does not know it is SQLite); only the adapter openBridgeState
//   actually returns implements io.Closer, so a state built some other way (a
//   test double) is left alone rather than assumed closeable.
//
// SPORT: internal/plugins:cascadepa-bridge-wiring (ADD) — P1-E23-W5-S48-T1.

import (
	"context"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// closeStateOnError releases the durable state when a later assembly step
// refuses, so a construction error never leaves the SQLite file open: the
// windows lane cannot remove a TempDir while a handle is held, and a real
// daemon would hold the exclusive lock across a retry. The original error
// is returned unchanged; a close failure is folded in as context.
func closeStateOnError(state cascadepa.BridgeState, err error) error {
	closer, ok := state.(io.Closer)
	if !ok {
		return err
	}
	if closeErr := closer.Close(); closeErr != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "cascade-pa bridge: assembly failed and closing the state also failed: %v", closeErr)
	}
	return err
}

// closeStateAfterStop wraps stop so the durable state openBridgeState opened
// is released once the module has actually drained, not before. The daemon
// reaches this through internal/daemon/subsystem_bridge.go's drainBridge,
// which already blocks until stop returns; leaving the handle open past that
// point is the exact defect Windows CI surfaces first, because it refuses to
// remove a temp directory that still holds cascade.db open.
//
// state is typed cascadepa.BridgeState (the plugin-facing seam, which does
// not know it is SQLite); only the adapter openBridgeState actually returns
// implements io.Closer, so a state built some other way (a test double) is
// left alone rather than assumed closeable.
func closeStateAfterStop(stop func(context.Context) error, state cascadepa.BridgeState) func(context.Context) error {
	closer, ok := state.(io.Closer)
	if !ok {
		return stop
	}
	return func(ctx context.Context) error {
		stopErr := stop(ctx)
		closeErr := closer.Close()
		if stopErr != nil {
			return stopErr
		}
		return closeErr
	}
}
