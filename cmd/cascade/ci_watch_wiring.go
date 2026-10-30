// Purpose: ciWatchEvents, the adapter that binds ci.CmdDeps.WatchEvents to
// (*client.Client).JobEvents (contract:ci-cli-attach). internal/ci cannot
// import internal/client (ci -> client -> daemon -> ci would cycle), so the
// ci mount file sets the binding here, at the composition root.
// Inputs: statusDeps, which resolve the daemon socket and its dialer.
// Outputs: a WatchEvents func yielding ci.CIEventFrame values.
// Constraints: a socket that cannot be resolved or a refused subscription is
// an error, never an empty stream. The closer cancels the subscription and
// the converter together.
// SPORT: cmd/cascade ci watch wiring (ADD, P1-CI-11).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/client"
)

// ciWatchEvents adapts JobEvents on the daemon socket deps resolve to
// ci.CmdDeps.WatchEvents.
func ciWatchEvents(deps statusDeps) func(context.Context, string) (<-chan ci.CIEventFrame, func(), error) {
	return func(ctx context.Context, jobID string) (<-chan ci.CIEventFrame, func(), error) {
		settings, err := resolveStatusSocket(ctx, deps)
		if err != nil {
			return nil, func() {}, err
		}
		ctx, cancel := context.WithCancel(ctx)
		frames, closeFrames, err := client.New(settings.SocketPath, deps.DialContext, 0).JobEvents(ctx, jobID)
		if err != nil {
			cancel()
			return nil, func() {}, err
		}
		out := make(chan ci.CIEventFrame)
		go func() {
			defer close(out)
			for f := range frames {
				select {
				case out <- ci.CIEventFrame{Event: f.Event, Data: []byte(f.Data)}:
				case <-ctx.Done():
					return
				}
			}
		}()
		return out, func() { cancel(); closeFrames() }, nil
	}
}
