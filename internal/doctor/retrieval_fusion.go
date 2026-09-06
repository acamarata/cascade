package doctor

// Purpose: the R-16.9 retrieval_fusion_default check — reports the
//   actionable note whenever the effective retrieval.fusion.enabled
//   resolves to false, whether that is F/S-12.T6's measured gate default
//   or an explicit operator override. It follows the census.go/
//   mcpcheck.go precedent already in this package: a Check constructed
//   over an injected interface, not yet registered with a live
//   CheckRegistry by a composition root, because no `cascade doctor`
//   composition root exists in the P1 tree yet — internal/daemon and
//   cmd/cascade, where one would call Register, are other agents' files_
//   scope in this wave. Registered and driven through the real
//   CheckRegistry + Runner (this ticket's own registry_test.go), which is
//   the production entry point every Check in this package currently has.
// Inputs: a FusionEnabledProvider (internal/runtime.Config satisfies it).
// Outputs: CheckResult — status=warn naming the R-16.9 fallback when
//   fusion is off; status=ok when it is on.
// Constraints: Art.1 — an unreadable config is status=error, never a
//   silent ok; this check never itself decides the default, only reports
//   what Config already resolved.
// SPORT: placeholder: doctor/framework (ADD, P1-E06-W2-S12-T6).

import "context"

// FusionEnabledProvider supplies the effective retrieval.fusion.enabled
// value. *runtime.Config satisfies this via its FusionEnabled field, read
// through a small adapter at the composition root (internal/runtime is
// not imported by this package, matching Art.10.2's layering: doctor
// checks depend on narrow interfaces, not on the packages they check).
type FusionEnabledProvider interface {
	// RetrievalFusionEnabled reports the effective retrieval.fusion.enabled.
	RetrievalFusionEnabled() bool
}

// retrievalFusionCheck implements Check for the retrieval_fusion_default
// probe.
type retrievalFusionCheck struct {
	config FusionEnabledProvider
}

// NewRetrievalFusionGateCheck builds the retrieval_fusion_default Check.
func NewRetrievalFusionGateCheck(config FusionEnabledProvider) Check {
	return &retrievalFusionCheck{config: config}
}

func (c *retrievalFusionCheck) Name() string { return "retrieval_fusion_default" }

func (c *retrievalFusionCheck) Describe() string {
	return "reports the R-16.9 fusion default: on when the evaluation gate measured a real recall@10 improvement, off with a fallback note otherwise"
}

func (c *retrievalFusionCheck) Metadata() CheckMeta {
	return CheckMeta{FirstRun: false, Fixable: false}
}

func (c *retrievalFusionCheck) Fix(context.Context) (FixResult, error) {
	return FixResult{}, ErrCheckNotFixable
}

// Run reports the effective fusion default. Status is never a function of
// WHY it is false (a failed gate vs. an explicit operator override look
// identical from Config alone) — the message says both are possible, and
// the doctor bundle's resolved-config section (bundle.go) already carries
// the raw retrieval.fusion.enabled source for an operator who wants to
// tell them apart.
func (c *retrievalFusionCheck) Run(_ context.Context) (CheckResult, error) {
	if c.config == nil {
		return CheckResult{Status: StatusError, Message: "no config provided to the retrieval fusion check"}, nil
	}
	if c.config.RetrievalFusionEnabled() {
		return CheckResult{Status: StatusOK, Message: "retrieval.fusion.enabled is on"}, nil
	}
	return CheckResult{
		Status:  StatusWarn,
		Message: "retrieval.fusion.enabled is off",
		Detail: "either the R-16.9 evaluation gate did not measure a >=10% relative recall@10 " +
			"improvement over FTS5-only with no known-item loss, or an operator set it explicitly",
		Remediation: "re-run the retrieval evaluation harness (internal/retrieval/eval) against a " +
			"real embedder recording, or set [retrieval.fusion] enabled = true to force it on",
	}, nil
}
