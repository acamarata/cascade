// Purpose: the index-health check set registered into the C/S-05.T2
// pluggable doctor framework: index presence, generation-marker currency,
// and a verify summary — surfaced through `cascade doctor`.
//
// SPORT: internal.retrieval.lifecycle.DoctorCheck/ADDED (P1-E06-W2-S11-T4).
package lifecycle

import (
	"context"
	"fmt"
	"os"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DoctorCheckName is this check's stable registry name.
const DoctorCheckName = "retrieval_index"

// ManagerBuilder lazily builds (and, via the returned closer, tears down)
// a Manager for one Run call. It is a function rather than a stored
// *Manager because doctor.Check construction (NewDoctorCheck, called at
// composition-root init time) must not block or error, while building a
// real Manager opens a database connection — exactly the same "own
// second connection" deferral internal/daemon/context_scope.go documents
// for the same reason.
type ManagerBuilder func(ctx context.Context) (m *Manager, closer func(), err error)

// DoctorCheck implements doctor.Check over a lazily built Manager.
type DoctorCheck struct {
	build ManagerBuilder
}

// NewDoctorCheck returns the index-health check backed by build. build
// may be nil (no retrieval index is configured on this build); Run then
// reports StatusError rather than panicking or silently passing (Art.1).
func NewDoctorCheck(build ManagerBuilder) doctor.Check { return DoctorCheck{build: build} }

// Name returns the check's stable registry name.
func (DoctorCheck) Name() string { return DoctorCheckName }

// Describe returns the check's human-readable one-liner.
func (DoctorCheck) Describe() string {
	return "checks the retrieval index is present, current, and consistent"
}

// Metadata declares this check as ordinary (not first-run: an index is
// commonly absent on a fresh install, which Run already reports as a
// warning rather than an error) and not fixable — rebuild is the
// documented repair path a human or a script invokes deliberately
// (R-21.189), never something `doctor --fix` triggers on its own.
func (DoctorCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: false, Fixable: false}
}

// Run reports the index's health.
func (c DoctorCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if c.build == nil {
		return doctor.CheckResult{Status: doctor.StatusError,
			Message: "no retrieval index is configured on this installation"}, nil
	}
	manager, closer, err := c.build(ctx)
	if err != nil {
		return doctor.CheckResult{Status: doctor.StatusError,
			Message: "could not open the retrieval index", Detail: err.Error()}, nil
	}
	if closer != nil {
		defer closer()
	}
	// An index that was never built is a normal, unconfigured state on a
	// fresh install — not a defect `cascade doctor` should fail on — so
	// this reports StatusOK with an informational remediation rather than
	// StatusWarn. Only a BUILT index that verify finds inconsistent (or a
	// stored/current marker mismatch) is a warning.
	if _, err := os.Stat(manager.catalogPath); os.IsNotExist(err) {
		return doctor.CheckResult{Status: doctor.StatusOK,
			Message:     "no retrieval index has been built yet",
			Remediation: "run `cascade recall index rebuild` to build one"}, nil
	}
	report, err := manager.Verify(ctx)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return doctor.CheckResult{Status: doctor.StatusOK,
				Message: "no retrieval index has been built yet", Remediation: "run `cascade recall index rebuild` to build one"}, nil
		}
		return doctor.CheckResult{Status: doctor.StatusError,
			Message: "could not verify the retrieval index", Detail: err.Error()}, nil
	}
	return doctorResultFrom(report), nil
}

// doctorResultFrom shapes Verify's report into a CheckResult.
func doctorResultFrom(report VerifyReport) doctor.CheckResult {
	if report.Clean() {
		return doctor.CheckResult{Status: doctor.StatusOK, Message: "retrieval index is current and consistent"}
	}
	return doctor.CheckResult{
		Status: doctor.StatusWarn,
		Message: fmt.Sprintf("retrieval index: %d missing, %d orphaned, %d vector-incomplete, marker %s",
			len(report.Missing), len(report.Orphaned), len(report.VectorIncomplete), report.MarkerStatus),
		Remediation: "run `cascade recall index rebuild` to repair",
	}
}

// Fix is unconditionally not fixable (Metadata().Fixable is false).
func (DoctorCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}
