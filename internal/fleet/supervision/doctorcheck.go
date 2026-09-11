package supervision

// Purpose (this file): the `cascade doctor` attention-queue check
// (ticket task 9), mirroring internal/nodes/doctor.go's Q/S-36.T2/
// R-14.91 pattern: a doctor.Check implementation this package's own
// composition-root registration point can Register into the C/S-05.T2
// CheckRegistry — no internal/doctor file is edited.
//
// Inputs: a *Store (domain-table reachability) and an Alive func
// (subscription liveness — see subscribe.go's Subscription.Alive).
// Outputs: doctor.CheckResult.
// Constraints: Art.1 — an unverifiable subject (a Store whose Scan
// errors) reports StatusError, never a silent StatusOK.
//
// SPORT: fleet.supervision.AttentionCheck/ADDED (P1-E18-W4-S39-T1).

import (
	"context"

	"github.com/acamarata/cascade/internal/doctor"
)

// AttentionCheckName is this check's stable registry name.
const AttentionCheckName = "attention-queue"

// AliveFunc reports whether the attention-queue event-bus subscription
// is currently live. Subscription.Alive (subscribe.go) satisfies this.
type AliveFunc func() bool

// AttentionCheck is the doctor.Check implementation.
type AttentionCheck struct {
	store *Store
	alive AliveFunc
}

// NewAttentionCheck constructs the check over store, reporting
// subscription liveness via alive. A nil alive is treated as "always
// reports not-live" (fail closed) rather than panicking.
func NewAttentionCheck(store *Store, alive AliveFunc) doctor.Check {
	if alive == nil {
		alive = func() bool { return false }
	}
	return AttentionCheck{store: store, alive: alive}
}

// Name returns the check's stable registry name.
func (AttentionCheck) Name() string { return AttentionCheckName }

// Describe returns the check's human-readable one-liner.
func (AttentionCheck) Describe() string {
	return "verifies the attention-queue domain table is reachable and its event-bus subscription is live"
}

// Metadata declares this check as ordinary and not fixable: there is no
// remediation doctor could apply to a table it cannot reach or a
// subscription that has not started, only a restart/investigate action.
func (AttentionCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: false, Fixable: false}
}

// Fix always returns ErrCheckNotFixable, per Metadata().Fixable == false.
func (AttentionCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run verifies the domain table is reachable (a Scan that returns no
// error, even over zero rows) and reports the subscription's liveness.
// A nil store or an errored Scan reports StatusError (Art.1: an
// unverifiable subject is never silently OK). An empty, reachable queue
// with a dead subscription reports StatusWarn — the table itself is
// fine, but nothing will ever enqueue into it.
func (c AttentionCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return doctor.CheckResult{}, err
	}
	if c.store == nil {
		return doctor.CheckResult{Status: doctor.StatusError, Message: "attention-queue store not configured"}, nil
	}
	if _, err := c.store.countAll(ctx); err != nil {
		return doctor.CheckResult{
			Status:  doctor.StatusError,
			Message: "attention-queue domain table is not reachable",
			Detail:  err.Error(),
		}, nil
	}
	if !c.alive() {
		return doctor.CheckResult{
			Status:      doctor.StatusWarn,
			Message:     "attention-queue table is reachable but the event-bus subscription is not live",
			Remediation: "restart the daemon; a subscriber only starts at daemon boot",
		}, nil
	}
	return doctor.CheckResult{Status: doctor.StatusOK, Message: "attention-queue table reachable, subscription live"}, nil
}
