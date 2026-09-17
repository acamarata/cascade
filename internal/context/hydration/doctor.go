package hydration

// Purpose: the doctor check that says how often prompt hydration silently
//   did nothing (P1-E16-W4-S34-T4, R-16.6a).
// Inputs: a store to read the degraded log from, and a clock.
// Outputs: OK, WARN or ERROR over the trailing DegradedWindow.
// Constraints: hydration fails OPEN by design, so a degraded hydration is
//   invisible to the user by construction — the session just proceeds
//   without context. This check is the only place that invisibility
//   becomes visible, which is why a store it cannot read is ERROR rather
//   than OK: a check that cannot verify its subject reports so (Art.1).
// SPORT: internal/context/hydration (ADD) — P1-E16-W4-S34-T4.

import (
	"context"
	"fmt"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/pkg/provider"
)

// The two R-16.6a thresholds, over DegradedWindow.
const (
	// WarnAbove is the count above which the check warns. Zero: one
	// degraded hydration in a day is worth a line, because the user saw
	// nothing at all when it happened.
	WarnAbove = 0
	// FailAbove is the count above which the check errors. Twenty in a
	// day is not an occasional slow disk; it is hydration that is not
	// working.
	FailAbove = 20
)

// CheckName is the check's stable slug.
const CheckName = "context-hydration"

// StoreFunc opens the store the check reads. It is a function rather than
// a value so the check can be registered at init, before any database
// exists, and resolve one only when it runs.
type StoreFunc func(ctx context.Context) (provider.Store, func(), error)

// Clock is the time source, kept local so this package never reads the
// wall clock directly (forbidigo).
type Clock interface{ Now() time.Time }

// Check reports degraded-hydration counts.
type Check struct {
	store StoreFunc
	clock Clock
}

var _ doctor.Check = (*Check)(nil)

// NewCheck builds the check over store and clock.
func NewCheck(store StoreFunc, clock Clock) *Check { return &Check{store: store, clock: clock} }

// Name is the check's slug.
func (*Check) Name() string { return CheckName }

// Describe is the one-liner shown beside the name.
func (*Check) Describe() string {
	return "how often prompt hydration degraded in the last 24 hours"
}

// Metadata tags the check.
//
// Not fixable: the remedy for degraded hydration is whatever made the
// slice fail — an unbuilt retrieval index, an unreachable daemon, a disk
// slow enough to blow the three-second budget — and none of those is
// something this check could put right on its own.
func (*Check) Metadata() doctor.CheckMeta { return doctor.CheckMeta{FirstRun: false, Fixable: false} }

// Fix is unconditionally refused; see Metadata.
func (*Check) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run counts degraded events over the trailing window.
func (c *Check) Run(ctx context.Context) (doctor.CheckResult, error) {
	if c.store == nil || c.clock == nil {
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     "hydration check is not wired to a store",
			Remediation: "this is a build defect, not a configuration problem; report it",
		}, nil
	}
	store, closeStore, err := c.store(ctx)
	if err != nil {
		return unverifiable(err), nil
	}
	if closeStore != nil {
		defer closeStore()
	}
	count, err := CountDegraded(ctx, store, c.clock.Now(), DegradedWindow)
	if err != nil {
		return unverifiable(err), nil
	}
	return resultFor(count), nil
}

// unverifiable renders the "could not check" outcome. It is ERROR, not
// OK: reporting a clean bill of health for a subject nobody looked at is
// the exact failure Art.1 names.
func unverifiable(err error) doctor.CheckResult {
	return doctor.CheckResult{
		Status:      doctor.StatusError,
		Message:     "could not read the hydration event log",
		Detail:      err.Error(),
		Remediation: "run `cascade doctor` with the daemon's data directory readable",
	}
}

// resultFor maps a count onto a status.
func resultFor(count int) doctor.CheckResult {
	switch {
	case count > FailAbove:
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     fmt.Sprintf("prompt hydration degraded %d times in the last 24h", count),
			Remediation: "check that the retrieval index is built (`cascade recall index rebuild`) and that the daemon is reachable",
		}
	case count > WarnAbove:
		return doctor.CheckResult{
			Status:      doctor.StatusWarn,
			Message:     fmt.Sprintf("prompt hydration degraded %d time(s) in the last 24h", count),
			Remediation: "sessions proceeded without Cascade context; see the context.hydration event log",
		}
	default:
		return doctor.CheckResult{Status: doctor.StatusOK, Message: "prompt hydration has not degraded in the last 24h"}
	}
}
