// Package policy (dryrun.go): Purpose: the observe-only simulator — the
//
//	answer to "what would happen if I did this", produced by running the
//	REAL evaluation and discarding its writes rather than by predicting it
//	a second time.
//
// Inputs: a DryRunInput carrying the live EvalRequest and an optional
//
//	sensitivity tier to resolve under.
//
// Outputs: a DryRunResult — the verdict, the rung, the deciding layer, the
//
//	subject's own applicable grants, the engine's explanation, and the
//	informational flags — plus the same error the live path would have
//	returned.
//
// Constraints: THERE IS ONE IMPLEMENTATION OF THE POLICY RULES AND IT IS
//
//	Engine.Evaluate. Simulate does not re-derive a verdict, a rung, a layer
//	or a reason; it calls Evaluate on an engine that differs from the
//	receiver in exactly one field — the approval sink — and reads the
//	outcome. A simulator that reimplemented any part of the decision would
//	be indistinguishable from a correct one until the day the two drifted,
//	and the user would by then have made decisions on the wrong one.
//
//	The one field that differs is the side-effect boundary. Evaluate's only
//	write is the ask-verdict enqueue, so the sink it writes through is
//	replaced with discardEffects, which asks the live queue the READ-ONLY
//	half of its admission question and stores nothing. Nothing else on the
//	path writes: the registry, the grant store and the autonomy profile are
//	all reads, and the ledger and the audit log are reached only from the
//	queue's own write half, which a simulation never enters.
//
//	The prediction is CONSERVATIVE where it cannot be exact: every place
//	the read-only preview can disagree with the live queue, it disagrees by
//	refusing (which downgrades the report to deny), never by admitting. A
//	report is therefore never more permissive than the action itself would
//	be. The two such places are named in discardEffects and previewEnqueue.
//
//	The report is SUBJECT-SCOPED: it carries the simulated subject's own
//	grants and nothing else, and a request that failed at the subject or
//	capability gate carries no grants at all, so a simulation cannot be
//	used to enumerate what somebody else holds or to probe the registry.
//
// SPORT: internal/policy Engine/CHANGED (P1-E09-W2-S18-T4).
package policy

import (
	"context"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Simulate predicts one action's outcome without performing it.
//
// It writes nothing: no audit row, no queue entry, no grant change, no
// ledger row. The engine it evaluates on is a shallow copy of the receiver
// with the approval sink swapped, so the receiver itself is untouched and
// two concurrent simulations cannot see each other.
//
// The returned error is the live path's own: a refusal that Evaluate
// reports as an error is reported as one here too, always alongside a deny
// report, so a caller that ignores the error still reads a refusal.
func (e *Engine) Simulate(ctx context.Context, in DryRunInput) (DryRunResult, error) {
	if e == nil {
		return deniedSimulation(L4, "no policy engine"),
			cascade.New(cascade.KindInvalidInput, "policy: nil engine")
	}
	if ctx == nil {
		return deniedSimulation(in.Request.Level, "a simulation needs a context"),
			cascade.New(cascade.KindInvalidInput, "policy: Simulate needs a context")
	}
	sink := &discardEffects{live: e.approvals}
	out, err := e.observeOnly(sink).Evaluate(ctx, in.Request)
	return e.report(ctx, in, out, sink, err), err
}

// observeOnly returns a copy of the engine whose only difference is the
// approval sink. The copy shares the three read-only collaborators by
// reference, so a simulation sees exactly the registry rows, grants and
// profile the live path would see at this instant — including a grant
// revoked a moment ago, since neither path caches.
//
// An engine with NO queue attached gets no sink either. That is the point:
// attaching one would make the simulated path take a branch the live path
// does not take, which is the first way a simulator drifts.
func (e *Engine) observeOnly(sink *discardEffects) *Engine {
	sim := &Engine{registry: e.registry, grants: e.grants, autonomy: e.autonomy}
	if e.approvals != nil {
		sim.approvals = sink
	}
	return sim
}

// report renders the outcome Evaluate produced. Every policy field is
// COPIED from the outcome rather than recomputed: the verdict, the rung,
// the deciding layer and the explanation are the engine's own words.
//
// The three fields that are not in the outcome are derived from sources
// that are themselves single implementations: elevation from
// internal/rpc's canonical §5.14 table, the reach from the same
// narrowerVisibility choke point the live carrier narrows through, and
// the audit flag from what the live queue would have recorded.
func (e *Engine) report(
	ctx context.Context, in DryRunInput, out EvalOutcome, sink *discardEffects, err error,
) DryRunResult {
	res := DryRunResult{
		Verdict:           safeVerdict(out.Verdict),
		ElevationRequired: rpc.IsElevated(in.Request.Verb, in.Request.Params),
		RiskLevel:         safeLevel(out.Level),
		MatchedRule:       out.Layer.String(),
		Explanation:       out.Reason,
		AutoAdvance:       out.AutoAdvance,
		WouldEmitAudit:    sink.wouldEmitAudit(),
	}
	// A request that never reached a policy question — an invalid subject,
	// an unregistered capability — is answered with the refusal alone. It
	// gets no grant list, because listing what the subject holds in answer
	// to a question the engine refused to consider would tell a prober
	// more than the live path ever tells it.
	if err != nil {
		res.EffectiveScope = corpus.VisibilityPrivate
		return res
	}
	res.ApplicableGrants = e.applicableGrants(ctx, in.Request, out)
	res.EffectiveScope = effectiveScope(res.ApplicableGrants, in.SensitivityOverride)
	return res
}

// applicableGrants lists the SIMULATED SUBJECT's standing grants on the
// requested capability, marking the one that decided.
//
// It reads through the same GrantStore the decision read, filtered to the
// one capability that was asked about: a report is an answer to the
// question asked, not an inventory. A store error yields no list rather
// than a partial one — a report must not imply that a subject holds
// nothing when the truth is that the store could not be read.
func (e *Engine) applicableGrants(ctx context.Context, req EvalRequest, out EvalOutcome) []GrantRef {
	held, err := e.grants.List(ctx, req.Subject)
	if err != nil {
		return nil
	}
	matched := out.Layer == LayerStandingGrant
	refs := make([]GrantRef, 0, len(held))
	for _, g := range held {
		if g.Capability != req.Capability {
			continue
		}
		refs = append(refs, GrantRef{
			Capability:  g.Capability,
			ScopeClass:  g.ScopeClass,
			ExpiresAt:   g.ExpiresAt,
			Conditional: len(g.Conditions) > 0,
			Matched:     matched,
		})
	}
	if len(refs) == 0 {
		return nil
	}
	return refs
}

// effectiveScope answers how far this action's material could travel: the
// narrower of the grant's own reach and the resolved sensitivity tier.
//
// Both fail-closed rules meet here and neither can be bypassed. An
// unresolvable tier is already the restricted class by the time
// resolveSensitivity returns, and narrowerVisibility collapses any
// unranked pairing to private, so no combination of a malformed grant and
// a malformed tier produces a class wider than either side.
func effectiveScope(grants []GrantRef, override corpus.VisibilityClass) corpus.VisibilityClass {
	tier := resolveSensitivity(override)
	if len(grants) == 0 {
		return narrowerVisibility(corpus.VisibilityPrivate, tier)
	}
	scope := grants[0].ScopeClass
	for _, g := range grants[1:] {
		scope = narrowerVisibility(scope, g.ScopeClass)
	}
	return narrowerVisibility(scope, tier)
}
