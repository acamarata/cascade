// Purpose: runNodeServe's startup-cancellation predicate, split into its
// own file so node_serve.go stays under the 300-line cap.
//
// SPORT: cmd/cascade/node (CHANGE, drain-on-cancel fix).
package main

import (
	"context"
	"errors"
)

// drainOrFail decides whether a STARTUP failure is a real failure or just
// the operator having cancelled while startup was still in flight.
//
// Cancellation is not an error. If ctx is already done AND err is that same
// cancellation, every startup step downstream of it will fail for the same
// reason — the presence event store's `sqlite: schema init ...: context
// canceled` being the one that actually bit — and reporting any of them as
// a failure would make a clean `cascade node serve` shutdown look broken.
//
// Both conditions are required on purpose. Returning nil merely because ctx
// is done would swallow a GENUINE startup error that happened to coincide
// with a cancellation, which is exactly the kind of silent pass this
// codebase rejects elsewhere; err must itself be the cancellation.
//
// Found by CI, not locally: the drain test allows startup a fixed 200ms
// before cancelling, which a developer machine and a quiet container both
// win and a loaded shared runner loses. That timing dependence is the
// test's own weakness and is noted there; this function fixes the
// behaviour the test was right to be checking.
func drainOrFail(ctx context.Context, err error) error {
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
