// Purpose: the mandatory first-run dry-run gate on tier-1 auto-advance —
//
//	the guarantee that the first auto-advance attempt for a given
//	{account, autonomy-profile} pair is predicted before it is permitted.
//
// WHERE IT RUNS. R-14.247 §7: at the ROUTER, wrapping the ask-resolution
//
//	stage (internal/events/routing's resolveAutoAdvance), handed the
//	policy.EvalRequest the router already built for its one live Evaluate.
//	It cannot sit inside the stage: ActionDescriptor deliberately carries
//	no command text (R-21.236), so a guard there could not build a
//	DryRunInput, and one that rebuilt the request would reintroduce
//	exactly what R-21.236 removed. Simulate is a prediction that discards
//	its writes; the verdict the caller acts on remains the one the
//	router's single Evaluate returned.
//
// Inputs: a DryRunSimulator (the consumer-side transcription of
//
//	(*policy.Engine).Simulate), the store-backed first-run flags, the
//	live autonomy profile, and the optional attention/audit sinks.
//
// Outputs: pass, or a typed refusal blocking the action's live execution;
//
//	the dry run's trace in the audit log; an attention item wherever a
//	human must know.
//
// Constraints (R-14.247 §4 — the failure directions are separate
//
//		questions and each is implemented exactly):
//	  - a flag READ failure is UNKNOWN: the dry run runs, nothing blocks. An
//	    unreadable store is never "the flag is set", and blocking on it would
//	    turn a broken store into a denial-of-service on the operator's own
//	    autonomy setting.
//	  - any non-nil error from Simulate BLOCKS live execution and queues
//	    attention. No flag is written, so the next attempt re-runs the dry run.
//	  - a flag WRITE failure after a successful dry run does NOT block: the
//	    dry run happened, so the guard's promise is kept. An attention item
//	    tells the operator the guard will fire again.
//
// The dry run's evaluation TRACE goes to audit.Writer, which is what an
// append-only log is for. The first_run_done FLAG is a keyed record in
// pkg/provider.Store under the audit domain (storage.DomainAudit) —
// audit.Writer has no read, so the flag cannot live behind it (R-14.247
// §3), following the policy.NewStoreGrants / policy.NewStoreDenyList
// precedent.
//
// SPORT: fleet.supervision.EnforceDryRunFirst/ADDED,
//
//	fleet.supervision.DryRunSimulator/ADDED,
//	fleet.supervision.FirstRunFlags/ADDED (P1-E18-W4-S39-T4, R-14.247).

package supervision

import (
	"context"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DryRunSimulator is the dry-run seam this package consumes. The method is
// transcribed character for character from (*policy.Engine).Simulate
// (internal/policy/dryrun.go): the CONSUMER declares the seam and the
// production engine satisfies it, and nothing is re-exported from
// internal/policy (R-14.247 §1).
type DryRunSimulator interface {
	Simulate(ctx context.Context, in policy.DryRunInput) (policy.DryRunResult, error)
}

// Compile-time proof that the production engine is the simulator the guard
// consumes.
var _ DryRunSimulator = (*policy.Engine)(nil)

// ProfileReader reports the autonomy profile currently in force, the same
// live read CeilingReader gives the ceiling — so a profile change is
// observed by the next action, not at construction. *policy.Controller
// satisfies it.
type ProfileReader interface {
	Profile() *policy.AutonomyProfile
}

// DryRunFirstBlockedCode is the stable, greppable identifier the blocking
// refusal carries, on the same terms as routing's RouteDeniedCode.
const DryRunFirstBlockedCode = "AUTOADVANCE_DRYRUN_BLOCKED"

// The outcomes the dry run's trace row can record. Unexported: they are
// the trace row's own vocabulary, and the row is this package's output.
const (
	tracePassed    = "dry_run_passed"
	traceBlocked   = "dry_run_blocked"
	traceFlagWrite = "flag_write_failed"
)

// dryRunActor names this gate in the audit log.
const dryRunActor = "supervision.dryrun_first"

// DryRunFirstGuard blocks the first tier-1 auto-advance attempt for each
// {account, autonomy-profile} pair until a dry run of that very action has
// succeeded. It holds no mutable state, so it is safe to call concurrently
// from every action origin; the durability lives in the flag store.
type DryRunFirstGuard struct {
	sim      DryRunSimulator
	flags    *FirstRunFlags
	profiles ProfileReader
	// attention and trace may be nil, the same degrade a partially-wired
	// daemon gets from AutoAdvanceRecorder: the decision stands, the trail
	// is shorter. Neither absence can change a decision.
	attention AttentionPusher
	trace     audit.Writer
}

// EnforceDryRunFirst builds the mandatory first-run gate.
//
// The guard is not configurable off: there is no switch, on the contract's
// own terms — a bypassable first-run check would be a placeholder where a
// guarantee was specified. The simulator, the flag store and the profile
// source are all required: each one changes what a call answers, and a
// guard missing one could only proceed by assuming.
func EnforceDryRunFirst(
	sim DryRunSimulator, flags *FirstRunFlags, profiles ProfileReader,
	attention AttentionPusher, trace audit.Writer,
) (*DryRunFirstGuard, error) {
	switch {
	case sim == nil:
		return nil, cascade.New(cascade.KindInvalidInput,
			"supervision: dry-run-first enforcement requires a simulator")
	case flags == nil:
		return nil, cascade.New(cascade.KindInvalidInput,
			"supervision: dry-run-first enforcement requires a first-run flag store")
	case profiles == nil:
		return nil, cascade.New(cascade.KindInvalidInput,
			"supervision: dry-run-first enforcement requires an autonomy profile source")
	}
	return &DryRunFirstGuard{
		sim: sim, flags: flags, profiles: profiles,
		attention: attention, trace: trace,
	}, nil
}

// Guard decides whether the ask-resolution stage may run for req.
//
// A nil error passes; a non-nil error blocks the action outright. It is
// called only where tier-1 could actually fire — an ask the stage is about
// to resolve — so an action the engine already allowed or denied never
// pays for a simulation it does not need.
func (g *DryRunFirstGuard) Guard(ctx context.Context, req policy.EvalRequest) error {
	account := req.Subject.String()
	version := profileVersion(g.profiles.Profile())
	// A flag the store could not answer for leaves done FALSE: UNKNOWN is
	// not "already done" (R-14.247 §4), and it is not a block either —
	// re-running a side-effect-free simulation is the conservative
	// direction, while blocking would turn an unreadable store into a
	// denial-of-service on the operator's own autonomy setting.
	done := false
	if flagged, err := g.flags.Done(ctx, account, version); err == nil {
		done = flagged
	}
	if done {
		return nil
	}
	res, simErr := g.sim.Simulate(ctx, policy.DryRunInput{Request: req})
	if simErr != nil {
		g.emitTrace(ctx, req, account, version, res, simErr, traceBlocked)
		g.queueAttention(ctx, req, KindPolicyAsk)
		return cascade.Wrapf(cascade.KindPolicyDenied, simErr,
			"supervision: the first-run dry run failed; live execution is blocked until one succeeds (%s)",
			DryRunFirstBlockedCode)
	}
	if err := g.flags.Mark(ctx, account, version); err != nil {
		// The dry run happened; the guard's promise ("never fire live on
		// the first attempt without a dry run") is kept. Do NOT block —
		// failing the action here would be fail-closed on preference, not
		// on authorization. Tell the operator the guard will fire again.
		g.emitTrace(ctx, req, account, version, res, nil, traceFlagWrite)
		g.queueAttention(ctx, req, KindError)
		return nil
	}
	g.emitTrace(ctx, req, account, version, res, nil, tracePassed)
	return nil
}

// queueAttention files the condition for a human.
//
// The attention queue is this tree's pending-review surface, and the trace
// row carries the detail the item itself has no room for (AttentionItem
// carries no free-text body). The item is filed under the global scope:
// the router has no session identity to narrow it to, and a first-run
// gate condition is the daemon's condition. A failed push is not
// returned — the blocking refusal is already the caller's answer, and a
// queue failure must not overwrite it with a different error.
func (g *DryRunFirstGuard) queueAttention(ctx context.Context, req policy.EvalRequest, kind Kind) {
	if g.attention == nil {
		return
	}
	_, _ = g.attention.Push(ctx, AttentionItem{
		Kind:      kind,
		SourceRef: actionRef(req),
		ScopeRef:  ScopeRef{Kind: scope.ScopeKindGlobal},
		Priority:  autoAdvancePriorityAsk,
	})
}

// actionRef names the action in trace rows and attention items. The router
// stamps the action's own ref into the request attributes it builds
// (routing.Action.request); a request without one falls back to the
// capability, so a queued item always names something.
func actionRef(req policy.EvalRequest) string {
	if ref := req.Attributes["ref"]; ref != "" {
		return ref
	}
	return req.Capability
}

// profileLevels are the rungs a profile resolves, in ladder order, for the
// version fingerprint.
var profileLevels = [...]policy.RiskLevel{policy.L0, policy.L1, policy.L2, policy.L3, policy.L4}

// profileVersion names the autonomy profile in force — R-14.56's
// "autonomy-profile-version". The tree has no version counter, so the
// identity is the profile's name plus a fingerprint of the table it
// actually resolves (SlotFor is the resolved view: ceilings and overlays
// already folded in). A profile change under any name re-triggers the dry
// run; an unchanged table never does.
// Both AutonomyProfile methods read here are nil-safe by design — a nil
// profile is the deny-everything "locked" table — so an unreadable profile
// is its own version rather than a panic or a shared default.
func profileVersion(p *policy.AutonomyProfile) string {
	v := p.Name()
	for _, lvl := range profileLevels {
		slot := p.SlotFor(lvl)
		v += "|" + lvl.String() + ":" + slot.Verdict.String() + ":" + slot.Source.String()
	}
	return v
}
