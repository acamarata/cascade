// Purpose: `cascade doctor`'s report for the chat topic engine
//
//	(P1-E21-W5-S46-T4 D2's "the doctor reports it" requirement). "No
//	embedder configured" is StatusOK -- an unconfigured OPTIONAL feature
//	on an otherwise healthy install, the same verdict internal/doctor/
//	checks_provider.go's classifyProviderHealth already gives "no
//	providers registered" (TestDoctorIsMountedOnRoot pins every mounted
//	check to StatusOK on a bare install; a check that WARNS by default
//	would fail that reachability proof for every install that has never
//	asked for topic segmentation, which is every install today).
//	StatusWarn is reserved for a HALF-configured state: an embedder
//	resolved but the retrieval store did not, which never happens from
//	today's resolvers (they gate together) but is a real, distinguishable
//	outcome the moment either resolver is replaced independently.
//
// Inputs: the same runtime.PathProvider doctor_mounts.go threads to every
//
//	other check's mount.
//
// Outputs: a doctor.Check registered under "chat_topics_engine".
// Constraints: reuses resolveChatSegmenterDeps/resolveChatRetrievalStore
//
//	rather than re-deriving the configured/not-configured verdict, so the
//	doctor report and chatTopicEngine's own runtime behaviour can never
//	disagree about which state they are in.
//
// SPORT: cmd/cascade chat topics doctor check (ADD) -- P1-E21-W5-S46-T4 D2.
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/runtime"
)

// chatTopicsDoctorCheck implements doctor.Check over the SAME resolver
// functions newChatTopicEngine uses, so the doctor's verdict tracks the
// real composition exactly.
type chatTopicsDoctorCheck struct {
	paths runtime.PathProvider
}

// newChatTopicsDoctorCheck returns the mountable check. paths may be nil
// only in a test fixture that never calls Run.
func newChatTopicsDoctorCheck(paths runtime.PathProvider) doctor.Check {
	return chatTopicsDoctorCheck{paths: paths}
}

func (c chatTopicsDoctorCheck) Name() string { return "chat_topics_engine" }

func (c chatTopicsDoctorCheck) Describe() string {
	return "reports whether the chat topic-segmentation engine has a real embedding provider composed"
}

func (c chatTopicsDoctorCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: false, Fixable: false}
}

// Run reports StatusOK both when the engine is fully configured and when
// no embedder is configured at all (the default, healthy state of every
// install today -- see this file's header). StatusWarn is reserved for
// the half-configured case: an embedder resolved but no retrieval store
// did.
func (c chatTopicsDoctorCheck) Run(ctx context.Context) (doctor.CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return doctor.CheckResult{Status: doctor.StatusError,
			Message: "context already done before the check could run", Detail: err.Error()}, nil
	}
	_, embedder, embedderReason := resolveChatSegmenterDeps(c.paths)
	if embedder == nil {
		return doctor.CheckResult{
			Status:  doctor.StatusOK,
			Message: "chat topic engine: no embedding provider configured (optional feature, turns are stored normally)",
			Detail:  embedderReason,
		}, nil
	}
	if store, storeReason := resolveChatRetrievalStore(c.paths); store == nil {
		return doctor.CheckResult{
			Status:      doctor.StatusWarn,
			Message:     "chat topic engine: an embedder is configured but no retrieval store is reachable",
			Detail:      storeReason,
			Remediation: "thread the daemon's provider.Store into wireChatHandlers (see chat_topics_engine.go's resolveChatRetrievalStore doc comment)",
		}, nil
	}
	return doctor.CheckResult{Status: doctor.StatusOK, Message: "chat topic engine is fully configured"}, nil
}

// Fix is not implemented: closing either gap is a composition-root code
// change, not a runtime repair `--fix` could apply.
func (c chatTopicsDoctorCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}
