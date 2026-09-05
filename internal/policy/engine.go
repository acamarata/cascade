// Package policy (engine.go): Purpose: the vocabulary of the evaluation
//
//	entry point — the request, the outcome, the layer names and the Engine
//	that binds the collaborators together. The evaluation itself is in
//	evaluator.go and the seven layers are in layers.go.
//
// Inputs: an EvalRequest carrying the subject, the capability name, the
//
//	command text the classifier resolves to a rung, the data class of the
//	material the action would move, and the request attributes standing
//	grants match against. NO caller supplies a risk level: the evaluator
//	resolves it once through the CommandClassifier seam.
//
// Outputs: an EvalOutcome (verdict, auto-advance, the level actually
//
//	evaluated, the layer that decided, why, and the Trace of every layer
//	that ran), plus the typed refusal on a terminal deny.
//
// Constraints: the layer order in evaluator.go is the security contract.
//
//	Layer 0 (data class) runs UNCONDITIONALLY and is not part of
//	first-match-wins, so no later layer can release it. Layers 1 to 6 are
//	first-match-wins in the R-14.26 order. The autonomy profile is
//	consulted LAST for anything no layer above settled, so it can restrict
//	what the lower layers permitted and can never release what they
//	refused.
//
//	LAYER 2 CONSULTS ONLY THE D/S-06.T3 IN-MEMORY ELEVATION NONCE LEDGER
//	(attestation replay). I/S-18.T3's ledger is approval-token replay and
//	is NEVER consulted by layer 2.
//
// SPORT: internal/policy Engine/ADDED, EvalRequest/ADDED,
//
//	EvalOutcome/ADDED, DecisionLayer/ADDED (P1-E09-W2-S18-T1);
//	DataClass/ADDED, Trace/ADDED, LayerResult/ADDED,
//	SameTurnAuthorizer/ADDED, ElevationVerifier/ADDED
//	(P1-E09-W2-S17-T2); denylist-defaults/CHANGE (P1-E09-W2-S17-T4).
package policy

import "github.com/acamarata/cascade/pkg/cascade"

// DecisionLayer names the layer that produced a verdict, so an audit row
// and `cascade policy explain` can say WHY an action decided the way it
// did rather than only what was decided.
type DecisionLayer uint8

// The seven layers, in R-21.236 order.
const (
	_ DecisionLayer = iota // 0 is deliberately not a valid layer

	// LayerDataClass is layer 0: the unconditional data-class check.
	LayerDataClass
	// LayerDenyList is layer 1: the never-allow set.
	LayerDenyList
	// LayerElevation is layer 2: the §5.14 elevated-verb check.
	LayerElevation
	// LayerStandingGrant is layer 3: an explicit matching grant.
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
	"", "data-class", "deny-list", "elevation", "standing-grant",
	"capability-default", "autonomy-profile", "fail-closed",
}

// String returns the layer's stable name, e.g. "autonomy-profile".
func (l DecisionLayer) String() string {
	if l < LayerDataClass || l > LayerFailClosed {
		return "invalid-decision-layer"
	}
	return decisionLayerNames[l]
}

// Index returns the layer's position in the seven-layer stack, 0 through 6.
// It is the enum value minus one, because 0 is reserved for the invalid
// zero value; an invalid layer reports the last index rather than the
// first, so a bad value can never be mistaken for the data-class layer.
func (l DecisionLayer) Index() uint8 {
	if l < LayerDataClass || l > LayerFailClosed {
		return uint8(LayerFailClosed) - 1
	}
	return uint8(l) - 1
}

// EvalRequest is one action awaiting a decision.
type EvalRequest struct {
	// Subject is who is acting.
	Subject Subject
	// Capability is the named capability the action needs. It must be
	// registered; an unknown name denies.
	Capability string
	// Attributes are the request facts a standing grant's conditions
	// match against.
	Attributes map[string]string
	// Verb is the RPC method name, when the action is one. Layer 2
	// classifies it through the canonical §5.14 table.
	Verb string
	// Action is the canonical text of what will run, and Params its
	// parameters. Action is the classifier's ONLY input: the evaluator
	// resolves the rung from it exactly once (R-21.236). The approval
	// queue hashes both and binds the approval to those digests, so an
	// approval cannot be spent on a request that changed after it was
	// shown.
	Action string
	Params []byte
	// DataClass is the sensitivity of the material this action would
	// move. An unset value reads as secret, the most restricted class.
	DataClass DataClass
	// LaneMaxDataClass is the most sensitive class the destination
	// tolerates. The zero value means NO external destination was
	// declared, i.e. the action stays on this machine, and layer 0 has
	// nothing to compare against. A caller that dispatches to a lane
	// MUST set it; a caller that runs locally leaves it unset.
	LaneMaxDataClass DataClass
	// ElevationNonce is the nonce from an ELEVATION_REQUIRED round trip,
	// and ParamsHash the digest it was bound to. Layer 2 verifies them
	// against the in-memory elevation ledger.
	ElevationNonce string
	ParamsHash     string
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
	// was folded in. It is never lower than the classified level.
	Level RiskLevel `json:"level"`
	// Layer names which layer decided.
	Layer DecisionLayer `json:"layer"`
	// Reason is a short human-readable explanation.
	Reason string `json:"reason"`
	// Trace is the layer-by-layer record of THIS evaluation. It is built
	// as the layers run, so it names the layer that actually decided.
	Trace Trace `json:"trace"`
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
	// classifier resolves the action text to a rung, ONCE per evaluation.
	// NewEngine attaches the real classifier; WithClassifier replaces it.
	classifier CommandClassifier
	// denyList is the configurable portion of layer 1. NewEngine attaches
	// DefaultDenyList (no configured rows); WithDenyList replaces it. The
	// unconditional portion is in layers.go and needs no collaborator.
	denyList DenyLister
	// sameTurn is the layer 1 override seam. NewEngine attaches
	// NoSameTurnAuth, so an engine nobody wired an authorizer into refuses
	// every deny-list entry rather than relying on a nil check to mean it.
	sameTurn SameTurnAuthorizer
	// elevation is the layer 2 verifier over the in-memory nonce ledger.
	elevation ElevationVerifier
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
//
// The command classifier is attached here rather than asked for, because
// there is exactly one of them and an engine without one could not resolve
// a rung at all.
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
	return &Engine{
		registry:   registry,
		grants:     grants,
		autonomy:   autonomy,
		classifier: NewCommandClassifier(),
		denyList:   DefaultDenyList(),
		sameTurn:   NoSameTurnAuth(),
	}, nil
}

// denyOutcome builds the refusal shape every terminal path returns, so no
// path can accidentally return a zero EvalOutcome — whose Verdict would be
// the invalid zero value rather than a deny.
func denyOutcome(level RiskLevel, layer DecisionLayer, reason string) EvalOutcome {
	out := EvalOutcome{
		Verdict:     VerdictDeny,
		AutoAdvance: false,
		Level:       safeLevel(level),
		Layer:       layer,
		Reason:      reason,
	}
	out.Trace = traceOf([]LayerResult{{
		Index:   layer.Index(),
		Layer:   layer,
		Verdict: VerdictDeny,
		Rule:    reason,
		Decided: true,
	}})
	return out
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
