// Purpose: the daemon's interim fan-out leg admission (contract:
//   fanout-producer): a bounded, ctx-aware semaphore shaped as a
//   WithPermitFn, one per daemon, shared by the conductor.execute fan-out
//   branch and the resume registration, until the reservation pipeline's
//   Reserver replaces it.
// Inputs: a slot count n.
// Outputs: a WithPermitFn that holds one slot for the duration of fn.
// Constraints: never unbounded and never a pass-through: n < 1 is clamped
//   to one slot. A caller whose ctx ends while it waits gets ctx's error
//   and fn never runs. The permit counts concurrent leg CALLS (one held
//   slot per Executor.Execute, its own retries included), never attempts.
// SPORT: conductor.fanout/CHANGE (P1-CORE-19).

package conductor

import "context"

// DefaultFanOutLegBudget is the number of fan-out legs one daemon runs at
// once when nothing else is configured.
const DefaultFanOutLegBudget = 4

// MaxFanOut is the largest fan_out a conductor.execute call may request.
// A larger value is refused as invalid input before any authorization or
// write, so one call can never queue an unbounded number of paid legs.
const MaxFanOut = 16

// NewLegBudget returns a WithPermitFn backed by n slots. A call blocks
// until a slot is free or ctx is done; on ctx done it returns ctx's error
// without running fn. The slot is released when fn returns, on every path.
func NewLegBudget(n int) WithPermitFn {
	if n < 1 {
		n = 1
	}
	slots := make(chan struct{}, n)
	return func(ctx context.Context, fn func(context.Context) error) error {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		defer func() { <-slots }()
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn(ctx)
	}
}
