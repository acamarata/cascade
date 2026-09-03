package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Purpose: the subsystem_census framework-shipped check (ticket contract
//
//	task 7b) — R-14.87's fail-loud subsystems rule made concrete: doctor
//	compares the bootstrap-declared subsystem manifest against live
//	state, and a declared-but-not-running subsystem is ALWAYS
//	status=error. Absence must never be implied by silence.
//
// Inputs: a SubsystemStateProvider (injected — D/S-06.T2's lifecycle
//
//	owns the real declared manifest and live state; this ticket wires
//	the interface and tests against fakes, since internal/runtime's
//	bootstrap.go is out of files_scope for this ticket).
//
// Outputs: CheckResult — status=error naming every declared subsystem
//
//	that is not currently running; status=ok only when every declared
//	subsystem is confirmed running (never inferred from an empty
//	"not running" list alone — see Run's doc comment).
//
// SPORT: placeholder: doctor/framework (ADD).

// SubsystemStateProvider supplies subsystem_census its two inputs:
// what SHOULD be running (per D/S-06.T2's bootstrap-declared manifest)
// and what IS running.
type SubsystemStateProvider interface {
	// DeclaredSubsystems returns the bootstrap-declared subsystem names.
	DeclaredSubsystems(ctx context.Context) ([]string, error)
	// RunningSubsystems returns the live-state set: true for a name that
	// is confirmed running, absent or false otherwise. A provider that
	// cannot determine a subsystem's live state at all must return an
	// error from this method rather than silently omitting the name —
	// Run treats a missing map entry as "not running" (Art.1: absence
	// is never a pass).
	RunningSubsystems(ctx context.Context) (map[string]bool, error)
}

// subsystemCensusCheck implements Check for the subsystem_census probe.
type subsystemCensusCheck struct {
	provider SubsystemStateProvider
}

// NewSubsystemCensusCheck builds the subsystem_census Check.
func NewSubsystemCensusCheck(provider SubsystemStateProvider) Check {
	return &subsystemCensusCheck{provider: provider}
}

func (c *subsystemCensusCheck) Name() string { return "subsystem_census" }

func (c *subsystemCensusCheck) Describe() string {
	return "compares the bootstrap-declared subsystem manifest against live state; a declared-but-missing subsystem is always an error"
}

func (c *subsystemCensusCheck) Metadata() CheckMeta {
	return CheckMeta{FirstRun: false, Fixable: false}
}

func (c *subsystemCensusCheck) Fix(context.Context) (FixResult, error) {
	return FixResult{}, ErrCheckNotFixable
}

// Run compares DeclaredSubsystems against RunningSubsystems. Every
// declared name not present-and-true in the running map is reported
// missing; status=error whenever the missing set is non-empty, and
// status=error (not ok) whenever either provider call itself fails,
// since a census that could not run proves nothing (Art.1 — an
// unverifiable subject is never a green tick).
func (c *subsystemCensusCheck) Run(ctx context.Context) (CheckResult, error) {
	declared, err := c.provider.DeclaredSubsystems(ctx)
	if err != nil {
		return CheckResult{Status: StatusError, Message: "could not read declared subsystem manifest", Detail: err.Error()}, nil
	}
	running, err := c.provider.RunningSubsystems(ctx)
	if err != nil {
		return CheckResult{Status: StatusError, Message: "could not read live subsystem state", Detail: err.Error()}, nil
	}

	var missing []string
	for _, name := range declared {
		if !running[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return CheckResult{
			Status:      StatusError,
			Message:     fmt.Sprintf("%d/%d declared subsystem(s) not running", len(missing), len(declared)),
			Detail:      strings.Join(missing, ", "),
			Remediation: "start the missing subsystem(s) or investigate why bootstrap did not bring them up",
		}, nil
	}
	return CheckResult{Status: StatusOK, Message: fmt.Sprintf("%d/%d declared subsystem(s) running", len(declared), len(declared))}, nil
}
