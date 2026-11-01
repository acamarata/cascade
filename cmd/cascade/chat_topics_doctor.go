// Purpose: `cascade doctor`'s report for the chat topic engine
//
//	(P1-E21-W5-S46-T4 D2; P1-CPA-12). Auto-filing off ([topics]
//	classifier_lane unset) is StatusOK: an unconfigured OPTIONAL feature on
//	a healthy install (TestDoctorIsMountedOnRoot pins every mounted check
//	to StatusOK on a bare install). StatusWarn is reserved for a
//	classifier lane that is configured but cannot be used: a refused
//	[topics] table, no embedder, or no retrieval store.
//
// Inputs: the same runtime.PathProvider doctor_mounts.go threads to every
//
//	other check's mount.
//
// Outputs: a doctor.Check registered under "chat_topics_engine".
// Constraints: reuses chatTopicDepsFor and chatTopicsReadiness, the
//
//	engine's own verdict, so the doctor and the engine never disagree.
//
// SPORT: cmd/cascade chat topics doctor check (ADD) -- P1-E21-W5-S46-T4 D2.
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/runtime"
)

// chatTopicsDoctorCheck implements doctor.Check over the SAME composition
// newChatTopicEngine uses.
type chatTopicsDoctorCheck struct {
	paths runtime.PathProvider
}

// newChatTopicsDoctorCheck returns the mountable check. paths may be nil.
func newChatTopicsDoctorCheck(paths runtime.PathProvider) doctor.Check {
	return chatTopicsDoctorCheck{paths: paths}
}

func (c chatTopicsDoctorCheck) Name() string { return "chat_topics_engine" }

func (c chatTopicsDoctorCheck) Describe() string {
	return "reports whether chat topic auto-filing has a classifier lane, an embedder and a store composed"
}

func (c chatTopicsDoctorCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: false, Fixable: false}
}

// Run reports StatusOK when auto-filing is off or fully configured, and
// StatusWarn when a classifier lane is set but the engine cannot use it.
func (c chatTopicsDoctorCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return doctor.CheckResult{Status: doctor.StatusError,
			Message: "context already done before the check could run", Detail: err.Error()}, nil
	}
	deps := chatTopicDepsFor(c.paths, runtime.NewSystemClock())
	reason := chatTopicsReadiness(deps)
	switch {
	case reason == "":
		return doctor.CheckResult{Status: doctor.StatusOK,
			Message: "chat topic engine is fully configured on lane " + deps.settings.ClassifierLane}, nil
	case deps.settingsErr == nil && !deps.settings.Enabled():
		return doctor.CheckResult{Status: doctor.StatusOK,
			Message: "chat topic engine: auto-filing is off (optional feature, turns are stored normally)",
			Detail:  reason}, nil
	default:
		return doctor.CheckResult{Status: doctor.StatusWarn,
			Message:     "chat topic engine: auto-filing is configured but cannot run",
			Detail:      reason,
			Remediation: "fix [topics] and [retrieval.embedding] in config.toml (see docs/config-reference.md)"}, nil
	}
}

// Fix is not implemented: closing a gap is a config or composition
// change, not a runtime repair `--fix` could apply.
func (c chatTopicsDoctorCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}
