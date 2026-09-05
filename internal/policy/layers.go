// Package policy (layers.go): Purpose: the seven layers of the policy
//
//	evaluation stack, one function each, plus the three seams later
//	tickets implement. evaluator.go sequences them; nothing here decides
//	the order.
//
// Inputs: an evalRun carrying the request, the rung the classifier already
//
//	resolved ONCE, and the engine's collaborators.
//
// Outputs: one LayerResult per layer, and a decided EvalOutcome from the
//
//	layer that settles the evaluation.
//
// Constraints: THE NORMATIVE ORDER (R-21.236, R-14.26) is
//
//	layer 0 — data-class check (UNCONDITIONAL, outside first-match-wins;
//	          a refusal is the terminal error ErrDataClassDenied)
//	layer 1 — deny-list
//	layer 2 — elevation check
//	layer 3 — standing grants
//	layer 4 — capability default policy
//	layer 5 — autonomy profile
//	layer 6 — fail-closed fallback
//
//	LAYER 2 CONSULTS ONLY THE D/S-06.T3 IN-MEMORY ELEVATION NONCE LEDGER
//	(attestation replay). I/S-18.T3's ledger is approval-token replay and
//	is NEVER consulted by layer 2.
//
//	A valid same-turn authorization at layer 1 does NOT allow and does NOT
//	terminate: it records the override and CONTINUES to layer 2, so every
//	§5.14 elevated verb still faces the elevation check (R-21.231).
//
// SPORT: internal/policy policy-engine/ADDED,
//
//	data-class-layer-zero/ADDED (P1-E09-W2-S17-T2).
package policy

import (
	"context"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// CodeDataClassDenied is the stable identifier of a layer 0 refusal
// (R-14.152: the taxonomy is frozen at fourteen kinds, so the contract's
// identifier survives as a string on the error).
const CodeDataClassDenied = "data-class-denied"

// ErrDataClassDenied is the terminal refusal layer 0 returns. It reuses
// this package's existing Code-comparing refusal type rather than adding a
// third one: the taxonomy error's own Is compares kinds alone, and this
// refusal shares KindPolicyDenied with several others.
var ErrDataClassDenied = &ClassifyError{
	Code:  CodeDataClassDenied,
	Cause: cascade.New(cascade.KindPolicyDenied, CodeDataClassDenied),
}

// layerDataClass is LAYER 0. It runs on every evaluation, before the
// deny-list and outside first-match-wins, so no later layer can release
// it: a standing grant authorizes the verb, never the disclosure.
//
// The comparison is between the class of the material and the ceiling the
// DESTINATION tolerates. A request with no declared destination is not
// leaving this machine, so there is nothing to compare against and the
// layer passes without deciding; a request that names a lane MUST carry
// the ceiling, and its own class reads as secret when unset.
func (c *evalRun) layerDataClass() (LayerResult, error) {
	if !c.req.LaneMaxDataClass.Valid() {
		return c.pass(LayerDataClass,
			"no external destination is declared, so no data class applies"), nil
	}
	class, ceiling := safeDataClass(c.req.DataClass), c.req.LaneMaxDataClass
	if class > ceiling {
		rule := class.String() + " material may not travel to a destination capped at " + ceiling.String()
		return LayerResult{
			Index: LayerDataClass.Index(), Layer: LayerDataClass,
			Verdict: VerdictDeny, Rule: rule, Decided: true,
		}, ErrDataClassDenied
	}
	return c.pass(LayerDataClass,
		class.String()+" material is within the "+ceiling.String()+" ceiling"), nil
}

// layerDenyList is LAYER 1: the never-allow set. The unconditional portion
// is §5.15's own sentence — a destructive or privileged action is
// deny-listed and reserved for same-turn authorization — so it needs no
// collaborator and cannot be configured away. The configurable portion is
// S-17.T4's DenyLister, consulted when one is attached; a deny-lister that
// errors is a deny, never a pass.
//
// A same-turn authorization overrides the ENTRY and nothing else: it
// records the override and returns undecided, so evaluation continues to
// layer 2 (R-21.231).
func (c *evalRun) layerDenyList(ctx context.Context) (LayerResult, *EvalOutcome) {
	rule, listed := c.denyListEntry(ctx)
	if !listed {
		return c.pass(LayerDenyList, "no deny-list entry matches"), nil
	}
	if c.sameTurnAuthorized(ctx) {
		c.sameTurn = true
		return c.pass(LayerDenyList,
			rule+", overridden by same-turn authorization; the elevation check still applies"), nil
	}
	return c.decide(LayerDenyList, VerdictDeny, false, rule)
}

// denyListEntry answers whether the action is on either portion of the
// deny-list, and names the entry that matched.
func (c *evalRun) denyListEntry(ctx context.Context) (string, bool) {
	if c.level == L4 {
		return "a destructive or privileged action is deny-listed and is authorized in the same turn only", true
	}
	if c.cfg.denyList == nil {
		return "", false
	}
	denied, err := c.cfg.denyList.Denied(ctx, c.req.Action)
	if err != nil {
		return "the deny-list could not be read, so the action is treated as listed", true
	}
	if denied {
		return "the configured deny-list names this action", true
	}
	return "", false
}

// sameTurnAuthorized consults the layer 1 override seam. No authorizer
// attached means nothing is same-turn authorized, and an authorizer that
// errors has not authorized anything either.
func (c *evalRun) sameTurnAuthorized(ctx context.Context) bool {
	if c.cfg.sameTurn == nil {
		return false
	}
	ok, err := c.cfg.sameTurn.Authorized(ctx, c.req.Subject, c.req.Action)
	return err == nil && ok
}

// layerElevation is LAYER 2. It classifies the verb through the canonical
// §5.14 table in internal/rpc — there is no second copy of that table in
// this package — and requires a verified attestation for anything it
// names. The layer is a GATE: it can refuse, and it can let evaluation
// continue, but it never allows on its own.
//
// It consults ONLY the D/S-06.T3 in-memory elevation nonce ledger.
// I/S-18.T3's ledger is approval-token replay and is never consulted here.
func (c *evalRun) layerElevation(ctx context.Context) (LayerResult, *EvalOutcome) {
	if !rpc.IsElevated(c.req.Verb, c.req.Params) {
		return c.pass(LayerElevation, "this verb is not elevation-class"), nil
	}
	if c.cfg.elevation == nil {
		return c.decide(LayerElevation, VerdictDeny, false,
			"an elevated verb needs a verified attestation and no elevation ledger is attached")
	}
	if c.req.ElevationNonce == "" {
		return c.decide(LayerElevation, VerdictDeny, false,
			"an elevated verb was requested with no elevation attestation")
	}
	err := c.cfg.elevation.Verify(ctx, ElevationAttestation{
		Nonce:      c.req.ElevationNonce,
		Method:     c.req.Verb,
		ParamsHash: c.req.ParamsHash,
	})
	if err != nil {
		return c.decide(LayerElevation, VerdictDeny, false,
			"the elevation attestation was refused: "+sanitize(err.Error()))
	}
	return c.pass(LayerElevation, "the elevation attestation verified"), nil
}

// layerStandingGrant is LAYER 3. It reads every grant kind the ONE
// S-17.T1 GrantStore API returns and yields the grant's own verdict.
//
// It reports undecided for every refusal from the store — no row, an
// expired row, an undecodable row, a condition the request does not
// satisfy — because "no standing grant applies" must fall through to the
// layers below rather than short-circuit them. Falling through can only
// reach the autonomy default, which is never more permissive than a grant
// would have been.
func (c *evalRun) layerStandingGrant(ctx context.Context) (LayerResult, *EvalOutcome) {
	d, err := c.cfg.grants.Check(ctx, CheckRequest{
		Subject:    c.req.Subject,
		Capability: c.req.Capability,
		Attributes: c.req.Attributes,
	})
	if err != nil || !d.Granted {
		return c.pass(LayerStandingGrant, "no standing grant applies"), nil
	}
	// The grant's verdict is still clamped by the §5.15 ceiling, because
	// that rung is reserved for same-turn authorization and a standing
	// grant is by definition not same-turn.
	verdict := maxVerdict(grantVerdict(d), hardVerdictFloor(c.level))
	return c.decide(LayerStandingGrant, verdict, c.autoAdvance(verdict),
		"a standing grant on "+capabilityLabel(c.req.Capability)+" matched")
}

// layerCapabilityDefault is LAYER 4: the capability's own action class.
// The class can only RAISE the evaluated level (the fold happens once, in
// evaluator.go), so this layer decides exactly when the capability was the
// binding constraint. The verdict is read from the profile's slot for the
// raised level, and the layer is named so an audit row explains the raise.
func (c *evalRun) layerCapabilityDefault(_ context.Context) (LayerResult, *EvalOutcome) {
	if !c.raisedByCapability {
		return c.pass(LayerCapabilityDefault,
			"the capability's own class did not raise the level"), nil
	}
	slot, ok := c.profileSlot()
	if !ok {
		return c.pass(LayerCapabilityDefault, "no autonomy profile is loaded"), nil
	}
	return c.decide(LayerCapabilityDefault, slot.Verdict, slot.AutoAdvance,
		"the capability's own class raised this action to "+c.level.String())
}

// layerAutonomyProfile is LAYER 5: the running profile's per-rung default.
// It applies only to actions no layer above settled, which is what makes
// the profile able to restrict what the lower layers permitted and unable
// to release what they refused.
func (c *evalRun) layerAutonomyProfile(_ context.Context) (LayerResult, *EvalOutcome) {
	slot, ok := c.profileSlot()
	if !ok {
		return c.pass(LayerAutonomyProfile, "no autonomy profile is loaded"), nil
	}
	return c.decide(LayerAutonomyProfile, slot.Verdict, slot.AutoAdvance,
		"profile "+c.cfg.autonomy.Profile().Name()+" sets "+
			c.level.String()+" to "+slot.Verdict.String())
}

// layerFailClosed is LAYER 6. Nothing above resolved the action, so the
// answer is the most restrictive one available. It always decides: an
// evaluation that reaches here has already exhausted every rule, and
// returning undecided would leave the caller with no verdict at all.
func (c *evalRun) layerFailClosed(_ context.Context) (LayerResult, *EvalOutcome) {
	reason := "no policy rule resolved this action, so the most restrictive answer applies"
	if _, ok := c.profileSlot(); !ok {
		reason = "no autonomy profile is loaded"
	}
	return c.decide(LayerFailClosed, VerdictDeny, false, reason)
}

// grantVerdict reads the verdict a matched grant yields. A decision that
// matched but names no verdict yields allow, which is what a grant record
// has meant since S-17.T1: the row's existence IS the authorization.
func grantVerdict(d Decision) Verdict {
	if d.Verdict.Valid() {
		return d.Verdict
	}
	return VerdictAllow
}
