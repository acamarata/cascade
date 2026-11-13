// Purpose: the call-reservation seam of the model.execute door. Execute and
//   ExecuteStream hold a reservation from ExecutorConfig.Reserver around
//   every dispatch: an unreserved request is reserved here and released after
//   the dispatch; a request naming a ReservationID is checked, never trusted
//   and never released by the door.
// Inputs: the request, tier and selection the door resolved.
// Outputs: a callHold the dispatch runs under, or an audited refusal.
// Constraints: no default reserver exists in this package; a nil Reserver
//   refuses every dispatch with ErrUnreservedDispatch and zero provider calls.

package conductor

import (
	"context"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// CallReserver reserves capacity for one model dispatch. Reserve opens a new
// hold for an unreserved request; Check resolves a hold an earlier caller
// reserved under id for jobID on sel, refusing an unknown or foreign one.
type CallReserver interface {
	Reserve(ctx context.Context, sel provider.Selection, req provider.ModelRequest) (HeldCall, error)
	Check(ctx context.Context, id string, jobID string, sel provider.Selection) (HeldCall, error)
}

// HeldCall is one held reservation. WithPermit runs fn while the call's
// permit is held; Release returns the hold with the usage the dispatch
// reported (reported is false when the provider reported none).
type HeldCall interface {
	ID() string
	WithPermit(ctx context.Context, fn func(context.Context) error) error
	Release(ctx context.Context, u provider.Usage, reported bool) error
}

// ErrUnreservedDispatch refuses a dispatch that has no call reservation: no
// Reserver is installed, or the Reserver answered with no hold.
var ErrUnreservedDispatch = cascade.New(cascade.KindUnavailable, "conductor: dispatch has no call reservation")

// callHold is the hold one dispatch runs under. An owned hold was reserved by
// the door and is released by it; a checked hold belongs to its caller.
type callHold struct {
	held  HeldCall
	owned bool
}

// holdCall resolves the reservation for one Execute or ExecuteStream dispatch
// after the door's deny and selection. A refusal is audited once.
func (e *Executor) holdCall(ctx context.Context, req provider.ModelRequest, tier provider.SensitivityTier, sel provider.Selection) (callHold, error) {
	h, err := e.resolveHold(ctx, req, sel)
	if err != nil {
		e.auditRefusal(ctx, req, tier, err)
	}
	return h, err
}

// resolveHold reserves an unreserved request or checks a named reservation.
func (e *Executor) resolveHold(ctx context.Context, req provider.ModelRequest, sel provider.Selection) (callHold, error) {
	r := e.pipeline.cfg.Reserver
	if r == nil {
		return callHold{}, ErrUnreservedDispatch
	}
	id := req.ReservationID
	if id == "" {
		held, err := r.Reserve(ctx, sel, req)
		if err != nil {
			return callHold{}, reserveRefusal(err)
		}
		if held == nil {
			return callHold{}, ErrUnreservedDispatch
		}
		return callHold{held: held, owned: true}, nil
	}
	held, err := r.Check(ctx, id, req.TaskID, sel)
	if err != nil {
		return callHold{}, cascade.Wrapf(cascade.KindPolicyDenied, err, "conductor: reservation %q refused", id)
	}
	if held == nil {
		return callHold{}, ErrUnreservedDispatch
	}
	if got := held.ID(); got != id {
		return callHold{}, cascade.Newf(cascade.KindPolicyDenied, "conductor: reservation %q refused: hold is %q", id, got)
	}
	return callHold{held: held}, nil
}

// reserveRefusal classifies a Reserve error. A governor refusal for a
// draining node or a missing or stale resource signal clears on its own, so
// it is named as such and keeps its sentinel in the chain; any other error
// keeps its own Kind, or is KindUnavailable when it carries none.
func reserveRefusal(err error) error {
	switch {
	case governor.IsDraining(err):
		return cascade.Wrap(cascade.KindUnavailable, err, "conductor: call reservation refused: node draining")
	case governor.IsSignalRefusal(err):
		return cascade.Wrap(cascade.KindUnavailable, err, "conductor: call reservation refused: no fresh resource signal")
	}
	if _, ok := cascade.KindOf(err); ok {
		return err
	}
	return cascade.Wrap(cascade.KindUnavailable, err, "conductor: reserving call")
}

// run executes dispatch while the hold's permit is held. An owned hold is
// then released, on success and failure alike, with the usage dispatch
// returned; a release error after a successful dispatch is KindInternal.
func (h callHold) run(ctx context.Context, dispatch func(context.Context) (provider.Usage, error)) error {
	var u provider.Usage
	err := h.held.WithPermit(ctx, func(c context.Context) error {
		var derr error
		u, derr = dispatch(c)
		return derr
	})
	if !h.owned {
		return err
	}
	rerr := h.held.Release(context.WithoutCancel(ctx), u, u != provider.Usage{})
	if err == nil && rerr != nil {
		return cascade.Wrap(cascade.KindInternal, rerr, "conductor: releasing call reservation")
	}
	return err
}

// retryHeld is withRetry run inside hold's permit. When the permit is never
// granted, the selection is sel and the stats and usage are zero.
func (e *Executor) retryHeld(ctx context.Context, hold callHold, req provider.ModelRequest, sel provider.Selection, run attemptFn, abandoned abandonFn) (provider.Selection, retryStats, provider.Usage, error) {
	finalSel, st := sel, retryStats{}
	var u provider.Usage
	err := hold.run(ctx, func(c context.Context) (provider.Usage, error) {
		var rerr error
		finalSel, st, u, rerr = e.withRetry(c, req, sel, run, abandoned)
		return u, rerr
	})
	return finalSel, st, u, err
}
