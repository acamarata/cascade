// Purpose: execution plumbing for the model.execute door (R-40.X8): the
//   JobID alias, the injected Router and ProviderResolver seams, the
//   security-pipeline collaborator interfaces (Classifier, TaskClassTable,
//   PolicyEvaluator, SensitivityGate), and the internal accounting types
//   (CostRecord, ExecutionTrace) execute.go's audit record uses. The SDK
//   types a caller actually sends across the wire - ModelRequest,
//   ModelResponse, Selection, SensitivityTier, and the ModelExecutor
//   interface this package's Executor implements - live exactly once, in
//   pkg/provider/model.go (J/S-19.T1); this file imports them rather than
//   redeclaring any of them.
// Inputs: none at this layer - these are contracts and thin plumbing
//   types, not behavior.
// Outputs: none.
// Constraints: no second JobID definition anywhere in this package
//   (R-21.217 as corrected by R-21.281); this package's only JobID name is
//   the alias below. No conductor-local copy of ModelRequest, ModelResponse,
//   Selection or SensitivityTier (R-21.264, TestExecute_ModelTypesLiveInPkgProvider).
// SPORT: conductor.execute/ADD (P1-E11-W3-S22-T1).

package conductor

import (
	"context"

	"github.com/acamarata/cascade/pkg/provider"
)

// JobID is the alias of the pkg/provider type J/S-19.T1 declares
// (R-21.281): the only JobID name in this package. Its values are minted
// with cascade.NewID() in execute.go; no second generator exists here.
type JobID = provider.JobID

// Router is the injected selection seam (R-21.217 E): execute.go never
// calls NewRouter and never owns the registry, QuotaPolicy or taxonomy
// table - the real implementation (K/S-22.T2) and its construction at the
// daemon composition root (K/S-22.T4) are both out of this ticket's scope.
// exclude lists lane ids barred from this selection, used for the single
// R-14.88 capability-denied failover this ticket owns (R-21.213).
type Router interface {
	Select(ctx context.Context, req provider.ModelRequest, exclude ...string) (provider.Selection, error)
}

// ProviderResolver turns a Router's Selection into the live
// provider.ModelProvider it named. It is the seam pipeline.go closes over
// to build the unexported execCapability (R-21.206 B): execute.go never
// holds a ProviderResolver or a ModelProvider directly, only the capability
// pipeline.go issues after the readiness gate and security checks pass.
// The real implementation (a lane-aware lookup over K/S-22.T2's registry
// reader) is out of this ticket's scope; _test.go supplies a fixed-mapping
// double.
type ProviderResolver interface {
	Resolve(ctx context.Context, sel provider.Selection) (provider.ModelProvider, error)
}

// Classifier resolves or confirms req before dispatch. Per R-21.208, any
// error Classify returns is a TERMINAL DENY: Execute maps it straight to
// cascade.ErrInvalidRequest and never enters an approval or elevation flow.
type Classifier interface {
	Classify(ctx context.Context, req provider.ModelRequest) error
}

// TaskClassTable is the six-collaborator taxonomy registry (R-21.206 A):
// the closed nine-row §5.16 table K/S-22.T4 owns. ResolveTaskClass's own
// validation in resolve.go is a pure function over the frozen nine names
// and does not consult this collaborator; its only role here is the
// construction-time readiness precondition - NewExecutor refuses a nil
// TaskClassTable.
type TaskClassTable interface {
	// Classes returns the registry's task-class rows. A real table is
	// non-empty; Ready() only checks the collaborator is present, not
	// that Classes() is non-empty, since this ticket ships no concrete
	// implementation to hold that invariant against.
	Classes() []string
}

// PolicyEvaluator is the Epic I policy.Authorize middleware seam (S-18.T5).
// That ticket is not a dependency of this one; NewExecutor requires a
// non-nil PolicyEvaluator so construction fails closed rather than
// defaulting to an always-allow no-op, and _test.go supplies a double.
type PolicyEvaluator interface {
	Authorize(ctx context.Context, req provider.ModelRequest) error
}

// SensitivityGate is the sensitivity-resolution collaborator R-21.206 A
// names (ResolveSensitivity plus the S-22.T3 EgressSubstitutor, neither of
// which this ticket depends on for its egress-substitution half). Resolve
// narrows or confirms tier under the same fail-closed rule ResolveSensitivity
// implements in resolve.go; the production implementation this ticket ships
// simply calls ResolveSensitivity, and NewExecutor still requires a non-nil
// value so a future substitution-aware implementation slots in without a
// signature change.
type SensitivityGate interface {
	Resolve(tier provider.SensitivityTier) provider.SensitivityTier
}

// ExecutionTrace is the per-call diagnostic trace this ticket's audit
// record carries in its Explain payload: which lane was selected and the
// outcome tag. It is conductor-internal, not returned to the RPC caller
// (R-40.X8 keeps ModelResponse itself in pkg/provider with no Trace
// field). The ticket's original CostRecord field (fan-out cost summation,
// R-21.214) is not declared here: it belongs on pkg/provider.ModelResponse
// per R-40.X8, and this ticket's files_scope forbids changing
// pkg/provider/model.go to add it - see the journal's contradiction log.
type ExecutionTrace struct {
	LaneID  string `json:"lane_id"`
	Outcome string `json:"outcome"`
	// Reason carries the taxonomy error's own message on a non-success
	// outcome (empty on success). It is the classified refusal reason,
	// never a raw request field or a credential value.
	Reason string `json:"reason,omitempty"`
}
