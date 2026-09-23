// Purpose (this file): the seven-kind logical CI requirement model
// (R-16.71) -- CIRequirement, RequirementKind, CIRequirementPlan, Target,
// Config and RequirementModel, the ONE requirement model in the tree.
// affected.go/affected_go.go/affected_cmd.go implement Affected's
// dispatch; selection.go implements the R-21.173 SelectTargets decision
// this type's Affected method feeds.
//
// Inputs: none at package scope -- a caller constructs a RequirementModel
// with a worktree root, a stack identifier ("go" is the one path this
// ticket implements a real subprocess for) and a Config, built from a
// loaded internal/runtime.Config via ConfigFromRuntime (config_runtime.go,
// final-confirm round 3 fix).
// Outputs: CIRequirement values a consumer queries per-kind via Requires,
// and CIRequirementPlan values RequirementModel.Affected's callers (T2's
// streaming dispatch, T3's executor resolution, selection.go's
// SelectTargets) build from a Target slice.
// Constraints: fail-closed zero-value (06 §5 rule 20) -- a zero
// CIRequirement is treated as all-kinds-required by every consumer
// (Requires below), never as "nothing required". RequirementKind's zero
// value is not a member of the closed seven-value enum.
// SPORT: internal.ci.CIRequirement/ADDED, internal.ci.RequirementKind/ADDED,
//
//	internal.ci.CIRequirementPlan/ADDED, internal.ci.RequirementModel/ADDED,
//	internal.ci.RequirementModel.Affected/ADDED, internal.ci.Target/ADDED,
//	internal.ci.Config/ADDED (P1-E32-W6-S65-T1).

package ci

import "context"

// RequirementKind is the typed string enum naming one of the seven CI
// check classes a CIRequirement governs. The zero value ("") is not a
// member of the closed set -- RequirementKind("").Valid() is false, so a
// forgotten/zeroed kind reads as a bug rather than silently meaning
// RequirementFormat.
type RequirementKind string

// The closed seven-member RequirementKind vocabulary this ticket's
// full_desc names.
const (
	RequirementFormat       RequirementKind = "format"
	RequirementLint         RequirementKind = "lint"
	RequirementCompile      RequirementKind = "compile"
	RequirementUnit         RequirementKind = "unit"
	RequirementIntegration  RequirementKind = "integration"
	RequirementArchitecture RequirementKind = "architecture"
	RequirementSecurity     RequirementKind = "security"
)

// allRequirementKinds is the closed enum's own enumeration, walked by
// Valid and by requirements_test.go's exhaustiveness assertion.
var allRequirementKinds = []RequirementKind{
	RequirementFormat, RequirementLint, RequirementCompile, RequirementUnit,
	RequirementIntegration, RequirementArchitecture, RequirementSecurity,
}

// Valid reports whether k is one of the seven closed RequirementKind
// members. The zero value and any unrecognised string are invalid.
func (k RequirementKind) Valid() bool {
	for _, v := range allRequirementKinds {
		if v == k {
			return true
		}
	}
	return false
}

// CIRequirement is a typed record of which of the seven CI check classes
// a given CI invocation must run. Every field defaults to Go's bool zero
// value (false), which is why Requires below treats the all-false zero
// struct as AllRequired rather than "nothing required": a caller that
// forgets to populate a CIRequirement must never silently skip every
// check (06 §5 rule 20).
//
// This ticket's contract (sport_updates, full_desc, tasks) names this
// type CIRequirement verbatim, and T2/T3 consume it by that exact name;
// renaming to Requirement to silence revive's package-name-stutter hint
// would break that contract.
//
//nolint:revive // contract-mandated exported name, see the doc comment above
type CIRequirement struct {
	Format       bool
	Lint         bool
	Compile      bool
	Unit         bool
	Integration  bool
	Architecture bool
	Security     bool
}

// AllRequired is the fail-closed sentinel value every field of which is
// true. It is what a zero CIRequirement resolves to (see Requires) and is
// also usable directly by a caller that means "run everything" (a
// High/Critical risk class, per selection.go's SelectTargets).
var AllRequired = CIRequirement{
	Format: true, Lint: true, Compile: true, Unit: true,
	Integration: true, Architecture: true, Security: true,
}

// isZero reports whether r is CIRequirement's Go zero value (every field
// false) -- the fail-closed trigger Requires checks.
func (r CIRequirement) isZero() bool {
	return r == CIRequirement{}
}

// Requires reports whether kind is required under r. Per 06 §5 rule 20,
// the all-false zero value is fail-closed: it resolves to AllRequired
// rather than "nothing required", so a consumer that receives an
// unpopulated CIRequirement (a forgotten field, a zero-initialized
// struct read off a stale record) cannot silently skip a kind. An
// invalid (non-enum) kind is likewise fail-closed to required=true --
// the same "never silently skip" reasoning applied to the KIND argument
// rather than the receiver.
func (r CIRequirement) Requires(kind RequirementKind) bool {
	if r.isZero() {
		r = AllRequired
	}
	switch kind {
	case RequirementFormat:
		return r.Format
	case RequirementLint:
		return r.Lint
	case RequirementCompile:
		return r.Compile
	case RequirementUnit:
		return r.Unit
	case RequirementIntegration:
		return r.Integration
	case RequirementArchitecture:
		return r.Architecture
	case RequirementSecurity:
		return r.Security
	default:
		return true
	}
}

// Target identifies one build target the affected-target computation can
// name: a Go import path for the Go stack, or a line of affected_cmd's
// stdout for a generic stack. TargetAll is the one non-concrete member --
// see its own doc comment.
type Target string

// TargetAll is the sentinel Target the "cannot compute a concrete
// affected set" paths return in place of an enumeration this ticket has
// no way to produce (an unrecognised stack, or a non-Go stack with no
// [ci].affected_cmd configured): a single-element []Target{TargetAll}
// means "run every target", not "run zero targets". It borrows Go's own
// "everything" pattern spelling so a caller piping Targets into
// tooling that already understands "..." as all is unsurprised by it.
const TargetAll Target = "..."

// Config is this ticket's own configuration view: [ci].affected_cmd only.
// It mirrors runner_config.go's LocalConfig precedent (a small package-
// local shape a caller populates from the runtime section), but unlike
// LocalConfig it gets ONE typed constructor here rather than an inline
// per-caller field copy: ConfigFromRuntime (config_runtime.go) is the
// ONE way a production Config is ever built from internal/runtime's
// parsed [ci].affected_cmd (final-confirm round 3 fix, AC
// "internal/ci/affected_cmd.go reads it from there, not from an
// unsourced field") -- every other construction (a bare Config{...}
// literal) is test-only. internal/ci already imports internal/runtime
// elsewhere in this package (runner.go, attention.go, run_cmd.go), so
// this is not a new dependency.
type Config struct {
	// AffectedCmd is the non-Go-stack affected-target command: run with
	// the changed-file list on stdin, one path per line, expected to
	// print target names on stdout, one per line. Empty means "no
	// command configured" -- affected.go's dispatch then returns the
	// conservative TargetAll fallback for any non-Go stack.
	AffectedCmd string
}

// CIRequirementPlan bundles one CI invocation's resolved requirement
// kinds, its target set, and (since R-21.173) the Selection that decided
// whether Targets is a concrete affected set or the TargetAll fallback,
// plus the candidate tree hash and risk class the decision was made
// against. It is the typed contract T2 (streaming dispatch) and T3
// (executor resolution) consume; this ticket adds no ci_results table
// and no CLI surface for it.
//
// Contract-mandated name (sport_updates: "internal/ci/CIRequirementPlan
// (new type)"); see CIRequirement's own doc comment on why it is not
// renamed to silence revive's stutter hint.
//
//nolint:revive // contract-mandated exported name, see the doc comment above
type CIRequirementPlan struct {
	Requirement CIRequirement
	Targets     []Target
	// Selection is R-21.173's resolved target-selection outcome. The zero
	// value is not meant to be read directly by a consumer that did not
	// go through SelectTargets -- call Selection.Resolved() first (see
	// selection.go).
	Selection TargetSelection
	// CandidateTreeHash is the AF/S-65.T2 immutable candidate snapshot
	// identity SelectTargets compared the live worktree against (R-21.147).
	// Recorded verbatim so AF/S-65.T4's attestation payload and the
	// AF/S-66.T2 receive gate can see exactly what was compared.
	CandidateTreeHash string
	// RiskClass is the AC/S-59.T4 classifier's risk-class string, carried
	// as a plain string (not internal/jobs.RiskClass) so this package
	// never imports internal/jobs -- see selection.go's own doc comment.
	RiskClass string
}

// RequirementModel is the ONE requirement model in the tree (R-16.71): a
// worktree root, a stack identifier, and the Config affected.go's
// non-Go-stack dispatch path reads. WorktreeRoot, Stack and Cfg are
// exported (unlike this file's other package-local helpers) because T2
// and T3 construct and pass a RequirementModel across package
// boundaries; there is no constructor function because there is no
// default to fill in -- every field is the caller's own input, verbatim.
type RequirementModel struct {
	WorktreeRoot string
	Stack        string
	Cfg          Config
}

// Affected returns the minimal set of Targets whose transitive inputs
// include at least one path in changed -- or the TargetAll fallback when
// that cannot be computed for m.Stack (affected.go's dispatch). Its
// signature is unchanged by R-21.173 (R-16.71): SelectTargets (below, in
// selection.go) is the caller that adds tree-hash and risk-class policy
// on top, never this method itself.
func (m RequirementModel) Affected(ctx context.Context, changed []string) ([]Target, error) {
	return affectedTargets(ctx, m.WorktreeRoot, m.Stack, m.Cfg, changed)
}
