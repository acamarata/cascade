package context

// Purpose: the `harness` doctor check (P1-E16-W4-S35-T3) — what
//   `cascade doctor --harness` reports.
// Inputs: a detector and a drift source, both injected.
// Outputs: one CheckResult summarising all three harnesses.
// Constraints: NOT having a harness installed is not a fault, so it is
//   StatusOK with the fact stated. Drift IS actionable, so it warns. A
//   platform where detection is unsupported by DESIGN reports OK naming
//   the tier; an environment the detector could not resolve reports
//   error, because that one is a subject nobody managed to look at
//   (Art.1). The check never mutates: detection is read-only and this
//   check does not regenerate anything, which is `harness sync`'s job.
// SPORT: internal/context harness doctor check (ADD) — P1-E16-W4-S35-T3.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/doctor"
)

// HarnessCheckName is the check's stable slug, and the value
// `doctor --harness` filters on.
const HarnessCheckName = "harness"

// DriftSource reports the drift-check result for a working directory. It
// is a seam so the check can be exercised without a real context cascade
// on disk.
type DriftSource func(ctx context.Context) (SyncResult, error)

// HarnessCheck reports harness detection and instruction drift.
type HarnessCheck struct {
	detector HarnessDetector
	drift    DriftSource
}

var _ doctor.Check = (*HarnessCheck)(nil)

// NewHarnessCheck builds the check. A nil drift source reports detection
// only, which is a genuinely useful answer on its own.
func NewHarnessCheck(detector HarnessDetector, drift DriftSource) *HarnessCheck {
	return &HarnessCheck{detector: detector, drift: drift}
}

// Name is the check's slug.
func (*HarnessCheck) Name() string { return HarnessCheckName }

// Describe is the one-liner shown beside the name.
func (*HarnessCheck) Describe() string {
	return "which coding harnesses are installed and whether their instruction files are current"
}

// Metadata tags the check.
//
// FirstRun: a fresh installation's first question is whether cascade
// found the harness the user actually works in.
//
// Not fixable HERE. Drift has a remedy — `cascade context harness sync` —
// but doctor's Fix runs without confirmation, and regenerating a user's
// instruction files as a side effect of a diagnostic is a bigger action
// than a diagnostic should take. The remediation names the command.
func (*HarnessCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: true, Fixable: false}
}

// Fix is unconditionally refused; see Metadata.
func (*HarnessCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run detects, then reports.
func (c *HarnessCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if c.detector == nil {
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     "the harness check is not wired to a detector",
			Remediation: "this is a build defect, not a configuration problem; report it",
		}, nil
	}
	states, err := c.detector.Detect(ctx)
	if err != nil {
		return c.detectionFailed(err), nil
	}
	if c.drift != nil {
		if result, derr := c.drift(ctx); derr == nil {
			states = WithDrift(states, result)
		}
	}
	return harnessResult(states), nil
}

// detectionFailed distinguishes the two ways detection can not happen.
//
// A platform this build does not resolve is a documented tier-2 state,
// not a broken installation, so it reports OK and says which. Anything
// else is a subject nobody managed to look at, which Art.1 says is an
// error rather than a silent pass.
func (c *HarnessCheck) detectionFailed(err error) doctor.CheckResult {
	if errors.Is(err, ErrHarnessDetectionUnsupported) {
		return doctor.CheckResult{
			Status:  doctor.StatusOK,
			Message: "harness detection is not available on this platform (tier-2)",
			Detail:  err.Error(),
		}
	}
	return doctor.CheckResult{
		Status:      doctor.StatusError,
		Message:     "harness detection could not run",
		Detail:      err.Error(),
		Remediation: "check that HOME (or XDG_CONFIG_HOME) is set for the user running cascade",
	}
}

// harnessResult renders the detected fleet.
func harnessResult(states []HarnessState) doctor.CheckResult {
	detected := DetectedKinds(states)
	drifted := driftedKinds(states)
	detail := harnessRows(states)

	if len(drifted) > 0 {
		return doctor.CheckResult{
			Status: doctor.StatusWarn,
			Message: fmt.Sprintf("%d of %d installed harnesses have drifted instruction files (%s)",
				len(drifted), len(detected), strings.Join(drifted, ", ")),
			Detail:      detail,
			Remediation: "run `cascade context harness sync` to regenerate them",
		}
	}
	if len(detected) == 0 {
		// Not a fault. A machine with no harness installed is a machine
		// cascade has nothing to wire into yet, and reporting that as a
		// problem would make every server install fail its own doctor.
		return doctor.CheckResult{
			Status:  doctor.StatusOK,
			Message: "no supported harness is installed",
			Detail:  detail,
		}
	}
	return doctor.CheckResult{
		Status:  doctor.StatusOK,
		Message: fmt.Sprintf("%d harness(es) installed and in sync", len(detected)),
		Detail:  detail,
	}
}

// driftedKinds names the installed harnesses whose files are stale.
func driftedKinds(states []HarnessState) []string {
	out := []string{}
	for _, s := range states {
		if s.Detected && s.Drift {
			out = append(out, string(s.Kind))
		}
	}
	sort.Strings(out)
	return out
}

// harnessRows renders one line per harness for the check's Detail.
//
// Every harness, always — including the absent ones. The whole point of
// this row in a doctor report is telling an operator what cascade can see,
// and a list that omitted what it could not see would answer a different
// question.
func harnessRows(states []HarnessState) string {
	rows := make([]string, 0, len(states))
	for _, s := range states {
		switch {
		case !s.Detected:
			rows = append(rows, string(s.Kind)+": not installed ("+s.InstallPath+")")
		case s.Drift:
			rows = append(rows, string(s.Kind)+": drifted — "+s.DriftReason)
		default:
			rows = append(rows, string(s.Kind)+": in sync")
		}
	}
	return strings.Join(rows, "; ")
}
