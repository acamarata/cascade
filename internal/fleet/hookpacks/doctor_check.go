package hookpacks

// Purpose (this file): the R-16.47/R-21.176 doctor.Check for the
//
//	completion-gate hook pack. It reports StatusError when neither
//	TaskCompleted nor Stop is registered under the "completion-gate" pack
//	name (the pack shipping unregistered is the same "built, tested,
//	unreachable" defect this phase keeps finding), and StatusWarn when a
//	live-daemon probe reports the daemon unreachable while a job is
//	active -- an unreachable daemon is otherwise a NO-OPINION outcome for
//	an individual completion (R-21.176), not a denial, but it is exactly
//	the condition an operator running `cascade doctor --harness` needs
//	surfaced.
//
// Inputs: a *HookRegistry to inspect, and an optional LivenessProbe.
// Outputs: doctor.CheckResult; this check is never Fixable.
// Constraints: doctor.Check implementations must be safe to construct at
//
//	composition-root init time (check.go's own doc comment) -- this type
//	does no I/O until Run is called. Mounting this check into `cascade
//	doctor`'s real registry (cmd/cascade/doctor_mounts.go) is
//	out-of-files_scope composition-root work, exactly the pattern this
//	ticket's own allow-list entries for NewDetector/GenerateCCInstructions
//	already document; see the journal and testonly-allow.json.
//
// SPORT: fleet/hookpacks.CompletionGateDoctorCheck/ADD (P1-E32-W6-S66-T1).

import (
	"context"

	"github.com/acamarata/cascade/internal/doctor"
)

// completionGateCheckName is this check's stable slug ([a-z0-9_-]+, per
// doctor.Check.Name's own contract).
const completionGateCheckName = "completion-gate-hooks"

// LivenessProbe reports whether the daemon is currently reachable and
// whether any job is active. The real implementation (cmd/cascade/hooks.go)
// dials the daemon's own socket and queries the jobs domain's Store via
// ListJobs for a non-terminal row; a nil LivenessProbe means the WARN leg
// of R-21.176 is simply not evaluated (the FAIL-on-unregistered leg still
// runs unconditionally).
type LivenessProbe func(ctx context.Context) (daemonReachable bool, jobActive bool, err error)

// CompletionGateDoctorCheck is the doctor.Check this ticket registers
// through the C/S-05.T2 registry (there is no doctor.Framework, R-16.47).
type CompletionGateDoctorCheck struct {
	registry *HookRegistry
	probe    LivenessProbe
}

// NewCompletionGateDoctorCheck constructs the check. probe may be nil.
func NewCompletionGateDoctorCheck(registry *HookRegistry, probe LivenessProbe) *CompletionGateDoctorCheck {
	return &CompletionGateDoctorCheck{registry: registry, probe: probe}
}

// Name implements doctor.Check.
func (c *CompletionGateDoctorCheck) Name() string { return completionGateCheckName }

// Describe implements doctor.Check.
func (c *CompletionGateDoctorCheck) Describe() string {
	return "the completion-gate CC hook pack (TaskCompleted/Stop) is registered and the daemon is reachable while jobs are active"
}

// Metadata implements doctor.Check. Never FirstRun-only (a harness that
// stops registering the pack mid-run matters just as much as at install
// time), never Fixable (installing the hook pack is P/S-34.T1's install
// wizard, not something this check can safely do on its own).
func (c *CompletionGateDoctorCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: false, Fixable: false}
}

// Fix implements doctor.Check. Always ErrCheckNotFixable, per Metadata.
func (c *CompletionGateDoctorCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run implements doctor.Check: StatusError when the pack (or either of
// its two required event types) is missing, else StatusWarn when the
// probe reports the daemon unreachable while a job is active, else
// StatusOK.
func (c *CompletionGateDoctorCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if !c.hasCompletionGatePack() {
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     "completion-gate hook pack is not registered",
			Remediation: "restart the daemon, or file a defect: RegisterCompletionHookPack was not called at startup",
		}, nil
	}
	if c.probe != nil {
		reachable, jobActive, err := c.probe(ctx)
		if err == nil && !reachable && jobActive {
			return doctor.CheckResult{
				Status:      doctor.StatusWarn,
				Message:     "daemon unreachable while a job is active",
				Remediation: "restart the daemon (cascade daemon run); active jobs cannot be completion-checked until it answers",
			}, nil
		}
	}
	return doctor.CheckResult{Status: doctor.StatusOK, Message: "completion-gate hooks registered"}, nil
}

// hasCompletionGatePack reports whether the "completion-gate" pack is
// registered and covers both required event types (R-16.48).
func (c *CompletionGateDoctorCheck) hasCompletionGatePack() bool {
	if c.registry == nil {
		return false
	}
	for _, pack := range c.registry.Packs() {
		if pack.Name != "completion-gate" {
			continue
		}
		hasTaskCompleted, hasStop := false, false
		for _, d := range pack.Descriptors {
			if d.EventType == EventTaskCompleted {
				hasTaskCompleted = true
			}
			if d.EventType == EventStop {
				hasStop = true
			}
		}
		return hasTaskCompleted && hasStop
	}
	return false
}

var _ doctor.Check = (*CompletionGateDoctorCheck)(nil)
