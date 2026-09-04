// Package policy (engine.go): Purpose: the evaluation entry point that
//
//	binds the layers of R-14.26 together — the capability registry, the
//	standing-grant store and the autonomy profile — and produces one
//	Verdict plus the auto-advance answer for one requested action.
//
// Inputs: an EvalRequest carrying the subject, the capability name, the
//
//	risk level ALREADY resolved once through the CommandClassifier seam
//	(R-14.26 resolves it before evaluation, never inside it), and the
//	request attributes standing-grant conditions match against.
//
// Outputs: an EvalOutcome (verdict, auto-advance, the level actually
//
//	evaluated, the layer that decided and why), plus the typed refusal on
//	a terminal deny.
//
// Constraints: the layer order is the security contract and every step is
//
//	a DENY step. The autonomy profile is consulted LAST (layer 5) and only
//	for actions no layer above it settled, so it can restrict what the
//	lower layers permitted and can never release what they refused: an
//	unregistered capability denies before any profile is read, a
//	capability whose own class sits higher than the classified level raises
//	the level rather than lowering it, and the L4 ceiling in autonomy.go
//	sits above every profile and overlay. Layers 1 (deny-list) and 2
//	(elevation) are S-17.T4's and the elevation ticket's; they compose in
//	FRONT of this entry point, which is why nothing here pretends to
//	implement them.
//
// SPORT: internal/policy Engine/ADDED, EvalRequest/ADDED,
//
//	EvalOutcome/ADDED, DecisionLayer/ADDED (P1-E09-W2-S18-T1).
package policy

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DecisionLayer names the R-14.26 layer that produced a verdict, so an
// audit row and `cascade policy explain` can say WHY an action decided the
// way it did rather than only what was decided.
type DecisionLayer uint8

// The layers this entry point implements, in R-14.26 order. Layers 1
// (deny-list) and 2 (elevation) are not members: they are separate
// tickets that compose in front of Evaluate, and naming them here without
// implementing them would be a claim the code does not keep.
const (
	_ DecisionLayer = iota // 0 is deliberately not a valid layer

	// LayerStandingGrant is R-14.26 layer 3: an explicit matching grant.
	LayerStandingGrant
	// LayerCapabilityDefault is layer 4: the capability's own action
	// class, which can only raise the evaluated level.
	LayerCapabilityDefault
	// LayerAutonomyProfile is layer 5: the per-risk-level default.
	LayerAutonomyProfile
	// LayerFailClosed is layer 6: nothing resolved, so the answer is the
	// most restrictive one available.
	LayerFailClosed
)

// decisionLayerNames holds each layer's stable name, indexed by value.
var decisionLayerNames = [...]string{
	"", "standing-grant", "capability-default", "autonomy-profile", "fail-closed",
}

// String returns the layer's stable name, e.g. "autonomy-profile".
func (l DecisionLayer) String() string {
	if l < LayerStandingGrant || l > LayerFailClosed {
		return "invalid-decision-layer"
	}
	return decisionLayerNames[l]
}

// EvalRequest is one action awaiting a decision.
type EvalRequest struct {
	// Subject is who is acting.
	Subject Subject
	// Capability is the named capability the action needs. It must be
	// registered; an unknown name denies.
	Capability string
	// Level is the risk level the classifier already resolved for this
	// action. An invalid value reads as L4 (safeLevel), so a caller that
	// forgets to set it gets the deny rung rather than the allow one.
	Level RiskLevel
	// Attributes are the request facts a standing grant's conditions
	// match against.
	Attributes map[string]string
	// Verb is the RPC method name, when the action is one. It lets the
	// approval queue refuse an elevation-class verb at its boundary
	// (§5.14) in addition to the elevation layer in front of Evaluate.
	Verb string
	// Action is the canonical text of what will run, and Params its
	// parameters. The approval queue hashes both and binds the approval
	// to those digests, so an approval cannot be spent on a request that
	// changed after it was shown.
	Action string
	Params []byte
	// Summary is the human-readable description an approval prompt shows.
	Summary string
}

// EvalOutcome is one decision.
type EvalOutcome struct {
	// Verdict is the decision (R-14.27).
	Verdict Verdict `json:"verdict"`
	// AutoAdvance reports whether an autonomous loop may proceed past
	// this action without a human turn. It requires BOTH an allow verdict
	// and the profile's own L0/L1 ceiling, so no layer can auto-advance
	// past what the running profile permits.
	AutoAdvance bool `json:"auto_advance"`
	// Level is the level actually evaluated, after the capability's class
	// was folded in. It is never lower than the requested level.
	Level RiskLevel `json:"level"`
	// Layer names which R-14.26 layer decided.
	Layer DecisionLayer `json:"layer"`
	// Reason is a short human-readable explanation.
	Reason string `json:"reason"`
	// ApprovalRequestID names the approval queue entry an ask verdict was
	// filed as, when a queue is attached. It is the ONLY approval field an
	// outcome carries: the token, the nonce and the action hash stay in
	// daemon memory (§5.24).
	ApprovalRequestID string `json:"approval_request_id,omitempty"`
}

// Engine evaluates actions against the policy layers. It holds no cache:
// every Evaluate re-reads the capability registry, the grant store and the
// running profile, so a revoked grant or a changed profile is in force on
// the very next call.
type Engine struct {
	registry CapabilityRegistry
	grants   GrantStore
	autonomy *Controller
	// approvals is the S-18.T3 sink an ask verdict is filed with. It is
	// optional and held as an INTERFACE: the engine never reaches into the
	// queue's implementation, and an engine with none attached still
	// evaluates identically.
	approvals ApprovalQueue
}

// NewEngine returns an Engine over the three collaborators. All three are
// required: an Engine missing one could only answer by assuming something
// about the layer it cannot consult, and every such assumption is a
// widening.
func NewEngine(registry CapabilityRegistry, grants GrantStore, autonomy *Controller) (*Engine, error) {
	if registry == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: engine needs a capability registry")
	}
	if grants == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: engine needs a grant store")
	}
	if autonomy == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: engine needs an autonomy controller")
	}
	return &Engine{registry: registry, grants: grants, autonomy: autonomy}, nil
}

// Evaluate decides one action.
//
// The order below is the contract:
//
//  1. the subject must name somebody, and the capability must be
//     REGISTERED — an unknown capability is a terminal deny that no
//     profile is consulted for;
//  2. the evaluated level is max(classified level, the capability's own
//     class rung) — folding the capability in can only raise it
//     (layer 4);
//  3. a standing grant that matches yields allow, still under the L4
//     ceiling (layer 3);
//  4. otherwise the running autonomy profile's slot for the evaluated
//     level decides (layer 5), and a profile that is missing or
//     unresolved denies (layer 6).
//
// A refused decision is returned as a verdict, not an error; an error
// accompanies only the terminal denies of step 1, where the caller is
// being told the request itself was not answerable.
func (e *Engine) Evaluate(ctx context.Context, req EvalRequest) (EvalOutcome, error) {
	if e == nil {
		return denyOutcome(L4, LayerFailClosed, "no policy engine"),
			cascade.New(cascade.KindInvalidInput, "policy: nil engine")
	}
	if err := req.Subject.Validate(); err != nil {
		return denyOutcome(safeLevel(req.Level), LayerFailClosed, "subject is not valid"), err
	}
	capDef, err := e.registry.Lookup(ctx, req.Capability)
	if err != nil {
		return denyOutcome(L4, LayerFailClosed, "capability is not registered"), err
	}
	level := maxLevel(req.Level, capDef.Class().Risk())
	out, ok := e.grantOutcome(ctx, req, level)
	if !ok {
		out = e.autonomyOutcome(level, level > safeLevel(req.Level))
	}
	// The S-18 seam, landed on top of T1's: an ask verdict is filed with
	// the approval queue, and a queue that refuses the action downgrades
	// the outcome to deny rather than leaving it at ask.
	return e.enqueueAsk(ctx, req, out), nil
}

// grantOutcome consults layer 3. It reports ok only for a grant that
// actually matched; every refusal from the store — no row, an expired row,
// an undecodable row, a condition the request does not satisfy — is "no
// standing grant applies", which falls through to the layers below rather
// than short-circuiting them. Falling through can only reach the autonomy
// default, which is never more permissive than a grant would have been.
func (e *Engine) grantOutcome(ctx context.Context, req EvalRequest, level RiskLevel) (EvalOutcome, bool) {
	d, err := e.grants.Check(ctx, CheckRequest{
		Subject:    req.Subject,
		Capability: req.Capability,
		Attributes: req.Attributes,
	})
	if err != nil || !d.Granted {
		return EvalOutcome{}, false
	}
	// The grant says allow; the L4 ceiling still applies, because §5.15
	// reserves destructive/privileged actions for same-turn authorization
	// and a standing grant is by definition not same-turn.
	verdict := maxVerdict(VerdictAllow, hardVerdictFloor(level))
	return EvalOutcome{
		Verdict:     verdict,
		AutoAdvance: e.autoAdvance(level, verdict),
		Level:       level,
		Layer:       LayerStandingGrant,
		Reason:      "a standing grant on " + capabilityLabel(req.Capability) + " matched",
	}, true
}

// autonomyOutcome consults layer 5, falling through to layer 6 when no
// profile is loaded.
func (e *Engine) autonomyOutcome(level RiskLevel, raisedByCapability bool) EvalOutcome {
	profile := e.autonomy.Profile()
	slot := profile.SlotFor(level)
	if profile == nil {
		return denyOutcome(level, LayerFailClosed, "no autonomy profile is loaded")
	}
	// When the capability's own class raised the level above the one the
	// classifier assigned, layer 4 is the binding constraint even though
	// the verdict is read from layer 5's slot for that raised level; the
	// outcome names layer 4 so the audit row explains the raise.
	layer := LayerAutonomyProfile
	if raisedByCapability {
		layer = LayerCapabilityDefault
	}
	return EvalOutcome{
		Verdict:     slot.Verdict,
		AutoAdvance: slot.AutoAdvance,
		Level:       level,
		Layer:       layer,
		Reason:      "profile " + profile.Name() + " sets " + level.String() + " to " + slot.Verdict.String(),
	}
}

// autoAdvance answers the §5.15 ceiling question for a layer that decided
// above the profile. It requires the profile's own permission as well as
// an allow verdict, so no layer auto-advances past what the running
// profile permits.
func (e *Engine) autoAdvance(level RiskLevel, v Verdict) bool {
	return safeVerdict(v) == VerdictAllow && e.autonomy.Profile().AllowsAutoAdvance(level)
}

// denyOutcome builds the refusal shape every terminal path returns, so no
// path can accidentally return a zero EvalOutcome — whose Verdict would be
// the invalid zero value rather than a deny.
func denyOutcome(level RiskLevel, layer DecisionLayer, reason string) EvalOutcome {
	return EvalOutcome{
		Verdict:     VerdictDeny,
		AutoAdvance: false,
		Level:       safeLevel(level),
		Layer:       layer,
		Reason:      reason,
	}
}

// capabilityLabel renders a capability name for a reason string without
// echoing an untrusted value verbatim.
func capabilityLabel(name string) string { return sanitize(name) }

// HasProfile reports whether a profile has been loaded. A caller that gets
// a deny with Layer == LayerFailClosed uses this to tell the operator to
// load a config instead of asking for a grant.
func (e *Engine) HasProfile() bool {
	return e != nil && e.autonomy.Profile() != nil
}
