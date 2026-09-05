// Package policy (evaluator.go): Purpose: the stack driver. It resolves
//
//	the rung ONCE, folds the capability's own class in, then runs the
//	seven layers of layers.go in their normative order and returns the
//	verdict together with the trace the run produced.
//
// Inputs: an EvalRequest and the engine's collaborators.
// Outputs: an EvalOutcome carrying the verdict, the deciding layer and the
//
//	Trace, and the terminal error on a refusal the caller must see.
//
// Constraints: there is exactly ONE Evaluate in this package and exactly
//
//	one place a RiskLevel is resolved (R-21.236). No caller supplies a
//	level and no caller receives a bare Verdict. The trace is built BY the
//	run, so the explanation names the layer that actually decided; it is
//	never reconstructed from the outcome afterwards.
//
// SPORT: internal/policy policy-engine/ADDED (P1-E09-W2-S17-T2).
package policy

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SameTurnAuthorizer answers whether an action carries same-turn
// authorization from the user. It is the seam S-17.T4's authorizer drops
// into. A same-turn hit at layer 1 overrides the deny-list ENTRY, records
// the override in the trace, and continues to layer 2: it never returns an
// allow and never skips the elevation check (R-21.231).
type SameTurnAuthorizer interface {
	// Authorized reports whether subject authorized action in this turn.
	// An error is not an authorization.
	Authorized(ctx context.Context, subject Subject, action string) (bool, error)
}

// ElevationAttestation is what layer 2 verifies: the nonce the daemon
// issued with ELEVATION_REQUIRED, bound to the method and parameter digest
// it was issued for.
type ElevationAttestation struct {
	// Nonce is the single-use nonce from the in-memory elevation ledger.
	Nonce string
	// Method is the RPC verb the nonce was issued for.
	Method string
	// ParamsHash is the digest of the parameters it was issued for.
	ParamsHash string
}

// ElevationVerifier verifies an elevation attestation against the
// D/S-06.T3 IN-MEMORY elevation nonce ledger, and only that ledger. A nil
// verifier is not a pass: layer 2 refuses every elevated verb when it has
// no ledger to check against.
type ElevationVerifier interface {
	// Verify consumes the attestation. A nil return authorizes the verb
	// for this one evaluation; any error refuses it.
	Verify(ctx context.Context, att ElevationAttestation) error
}

// WithDenyList attaches the configurable deny-list to layer 1 and returns
// the engine. Passing nil detaches it; the unconditional never-allow set
// in layers.go applies either way.
func (e *Engine) WithDenyList(d DenyLister) *Engine {
	if e == nil {
		return nil
	}
	e.denyList = d
	return e
}

// WithSameTurnAuthorizer attaches the layer 1 override seam and returns
// the engine. Passing nil detaches it, which means no action is
// same-turn authorized.
func (e *Engine) WithSameTurnAuthorizer(a SameTurnAuthorizer) *Engine {
	if e == nil {
		return nil
	}
	e.sameTurn = a
	return e
}

// WithElevationVerifier attaches the layer 2 verifier over the in-memory
// elevation nonce ledger and returns the engine. Passing nil detaches it,
// which makes layer 2 refuse every elevated verb.
func (e *Engine) WithElevationVerifier(v ElevationVerifier) *Engine {
	if e == nil {
		return nil
	}
	e.elevation = v
	return e
}

// evalRun is the state of ONE evaluation: the request, the levels resolved
// for it, the layer results so far, and the engine whose collaborators the
// layers read. It exists per call and is never shared, which is what lets
// the layers record into it without a lock.
type evalRun struct {
	// cfg is the collaborator set this run reads. The layers reach
	// nothing else: an evaluation sees exactly what is named here.
	cfg evaluatorConfig
	req EvalRequest
	// classified is the rung the command classifier resolved from the
	// action text, exactly once.
	classified RiskLevel
	// level is the rung actually evaluated: max(classified, the
	// capability's own class). It is never lower than classified.
	level RiskLevel
	// raisedByCapability records whether the capability was the binding
	// constraint, which is what makes layer 4 the deciding layer.
	raisedByCapability bool
	// sameTurn records that layer 1's deny-list entry was overridden by a
	// same-turn authorization (R-21.231).
	sameTurn bool
	// results is the trace under construction, in evaluation order.
	results []LayerResult
}

// evaluatorConfig is the collaborator set one evaluation reads. It is the
// engine's own fields, named as a set so the stack driver's dependencies
// are stated rather than implied.
type evaluatorConfig struct {
	registry   CapabilityRegistry
	grants     GrantStore
	autonomy   *Controller
	classifier CommandClassifier
	denyList   DenyLister
	sameTurn   SameTurnAuthorizer
	elevation  ElevationVerifier
}

// config returns the collaborator set this engine evaluates with.
func (e *Engine) config() evaluatorConfig {
	return evaluatorConfig{
		registry: e.registry, grants: e.grants, autonomy: e.autonomy,
		classifier: e.classifier, denyList: e.denyList,
		sameTurn: e.sameTurn, elevation: e.elevation,
	}
}

// Evaluate decides one action. It is the ONLY evaluation entry point in
// this package: the daemon, the CLI and the dry-run simulator all reach
// this function, and none of them classifies anything itself.
//
// The order below is the contract:
//
//  1. the subject must name somebody, and the capability must be
//     REGISTERED — an unknown capability is a terminal deny that no layer
//     is consulted for;
//  2. the rung is resolved ONCE from the action text, and the evaluated
//     level is max(that rung, the capability's own class);
//  3. layer 0 runs unconditionally, then layers 1 to 6 run in order and
//     the first to decide wins.
//
// A refused decision is returned as a verdict, not an error; an error
// accompanies only the terminal refusals, where the caller is being told
// the request itself was not answerable.
func (e *Engine) Evaluate(ctx context.Context, req EvalRequest) (EvalOutcome, error) {
	if e == nil {
		return denyOutcome(L4, LayerFailClosed, "no policy engine"),
			cascade.New(cascade.KindInvalidInput, "policy: nil engine")
	}
	if err := req.Subject.Validate(); err != nil {
		return denyOutcome(L4, LayerFailClosed, "subject is not valid"), err
	}
	capDef, err := e.registry.Lookup(ctx, req.Capability)
	if err != nil {
		return denyOutcome(L4, LayerFailClosed, "capability is not registered"), err
	}
	run := &evalRun{cfg: e.config(), req: req}
	run.classified = e.classify(ctx, req.Action)
	run.level = maxLevel(run.classified, capDef.Class().Risk())
	run.raisedByCapability = run.level > run.classified
	out, err := run.stack(ctx)
	if err != nil {
		return out, err
	}
	return e.enqueueAsk(ctx, req, out), nil
}

// classify resolves the action text to a rung, exactly once per
// evaluation. A classifier refusal is not an error the caller sees: an
// unclassifiable action IS a classification, and §5.15 says which one.
// This is also the path an engine with no classifier attached takes, so a
// missing classifier costs the top rung rather than a silent pass.
func (e *Engine) classify(ctx context.Context, action string) RiskLevel {
	if e.classifier == nil {
		return L4
	}
	level, err := e.classifier.Classify(ctx, action)
	if err != nil {
		return L4
	}
	return safeLevel(level)
}

// stack runs the seven layers. Layer 0 is unconditional and terminal on
// refusal; layers 1 to 6 are first-match-wins in the normative order.
func (r *evalRun) stack(ctx context.Context) (EvalOutcome, error) {
	zero, err := r.layerDataClass()
	r.results = append(r.results, zero)
	if err != nil {
		return r.outcome(r.results, zero, VerdictDeny, false), err
	}
	for _, layer := range r.ordered() {
		result, out := layer(ctx)
		r.results = append(r.results, result)
		if out != nil {
			return *out, nil
		}
	}
	// Unreachable while layer 6 always decides; if it ever stops doing
	// so, the answer is still a refusal rather than a zero outcome.
	result, _ := r.decide(LayerFailClosed, VerdictDeny, false,
		"the evaluation stack returned no verdict")
	r.results = append(r.results, result)
	return r.outcome(r.results, result, VerdictDeny, false), nil
}

// ordered returns layers 1 to 6 in the R-14.26 order. It is a slice rather
// than a chain of calls so the order is one readable list, and so the
// ordering test can assert the list itself.
func (r *evalRun) ordered() []func(context.Context) (LayerResult, *EvalOutcome) {
	return []func(context.Context) (LayerResult, *EvalOutcome){
		r.layerDenyList,
		r.layerElevation,
		r.layerStandingGrant,
		r.layerCapabilityDefault,
		r.layerAutonomyProfile,
		r.layerFailClosed,
	}
}

// pass records a layer that ran and did not decide. Its verdict is the
// invalid zero value, which reads as "settled nothing" and never as allow.
func (r *evalRun) pass(layer DecisionLayer, rule string) LayerResult {
	return LayerResult{Index: layer.Index(), Layer: layer, Rule: rule}
}

// decide records a layer that settled the evaluation and builds the
// outcome from the SAME LayerResult, so the explanation and the verdict
// cannot disagree.
func (r *evalRun) decide(
	layer DecisionLayer, v Verdict, autoAdvance bool, rule string,
) (LayerResult, *EvalOutcome) {
	result := LayerResult{
		Index: layer.Index(), Layer: layer,
		Verdict: safeVerdict(v), Rule: rule, Decided: true,
	}
	out := r.outcome(append(r.results, result), result, safeVerdict(v), autoAdvance)
	return result, &out
}

// outcome assembles the outcome from the deciding LayerResult and the
// trace the run built. Every field the caller reads comes from the run,
// not from a second derivation of it.
func (r *evalRun) outcome(
	results []LayerResult, deciding LayerResult, v Verdict, autoAdvance bool,
) EvalOutcome {
	verdict := safeVerdict(v)
	return EvalOutcome{
		Verdict:     verdict,
		AutoAdvance: autoAdvance && verdict == VerdictAllow,
		Level:       safeLevel(r.level),
		Layer:       deciding.Layer,
		Reason:      deciding.Rule,
		Trace:       traceOf(results),
	}
}

// profileSlot reads the running profile's slot for the evaluated level,
// reporting false when no profile is loaded.
func (r *evalRun) profileSlot() (Slot, bool) {
	profile := r.cfg.autonomy.Profile()
	if profile == nil {
		return Slot{}, false
	}
	return profile.SlotFor(r.level), true
}

// autoAdvance answers the §5.15 ceiling question for a layer that decided
// above the profile. It requires the profile's own permission as well as
// an allow verdict, so no layer auto-advances past what the running
// profile permits.
func (r *evalRun) autoAdvance(v Verdict) bool {
	return safeVerdict(v) == VerdictAllow &&
		r.cfg.autonomy.Profile().AllowsAutoAdvance(r.level)
}
