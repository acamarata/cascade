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
	"errors"
	"fmt"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/pkg/cascade"
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

// CountFunc reports how many degraded hydrations happened inside the
// window. It is a function rather than a store handle for a reason the
// doctor runner forced: checks run CONCURRENTLY, and the event log lives
// in a database whose driver takes an exclusive lock, so a check that
// opened it for itself would fail every sibling check that wanted the
// same file — and would fail outright whenever the daemon held it, which
// is the normal state.
//
// The composition root supplies an implementation that asks the daemon
// when one is live and reads the log directly otherwise, the same
// daemon-or-embedded split every other verb uses.
type CountFunc func(ctx context.Context) (int, error)

// Clock is the time source, kept local so this package never reads the
// wall clock directly (forbidigo).
type Clock interface{ Now() time.Time }

// Check reports degraded-hydration counts.
type Check struct {
	count CountFunc
	clock Clock
}

var _ doctor.Check = (*Check)(nil)

// NewCheck builds the check over count and clock.
func NewCheck(count CountFunc, clock Clock) *Check { return &Check{count: count, clock: clock} }

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
	if c.count == nil || c.clock == nil {
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     "hydration check is not wired to an event-log reader",
			Remediation: "this is a build defect, not a configuration problem; report it",
		}, nil
	}
	count, err := c.count(ctx)
	if errors.Is(err, ErrLogBusy) {
		return logBusy(err), nil
	}
	if err != nil {
		return unverifiable(err), nil
	}
	return resultFor(count), nil
}

// ErrLogBusy reports that the event log exists and is held exclusively by
// another component of this installation.
//
// It is a distinct error because it is a distinct FACT. The store driver
// is single-owner by design (an exclusive flock), so a held log means the
// system is running, not broken — and `cascade doctor` failing on a
// machine whose daemon is up would be a diagnostic that reports its own
// success condition as a fault.
var ErrLogBusy = cascade.New(cascade.KindConflict,
	"the hydration event log is held by another component of this installation")

// logBusy renders that fact. StatusOK, because nothing is wrong — and
// with the message saying plainly that no count was taken, so it is not
// a silent pass over an unmeasured subject.
func logBusy(err error) doctor.CheckResult {
	return doctor.CheckResult{
		Status:      doctor.StatusOK,
		Message:     "degraded hydrations were not counted: the event log is in use",
		Detail:      err.Error(),
		Remediation: "a running daemon answers this count itself; to read the log directly, stop the daemon first",
	}
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
