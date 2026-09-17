// Purpose: tier 3, suggest-only — the supervision tier that never
//
//	executes. Every action that reaches it becomes a Suggestion a human
//	reads later, and the executor is not called at all.
//
// WHY IT IS NOT A DENY. A deny tells the agent its action was refused, and
//
//	an agent that is refused retries or works around. A suggestion tells it
//	the action is waiting for a person, which is a different instruction and
//	the reason tier 3 exists as a tier rather than as a policy verdict.
//
// WHERE THE DETAIL LIVES. AttentionItem carries no free-text body by
//
//	design, so the Suggestion's own fields — what was proposed, at what
//	rung, and why — are written to the append-only audit log as the
//	rationale payload, and the queued item points at the same action ref.
//	That is the identical split dryrun_trace.go already uses, and it is why
//	neither half duplicates the other: the queue is the worklist, the log
//	is the record.
//
// Inputs: the attention queue and the audit writer, both optional in the
//
//	sense that a nil one shortens the trail; neither can change the
//	outcome, because tier 3's outcome is fixed.
//
// Outputs: the Suggestion that was filed.
// Constraints: no executor reference exists in this file. Tier 3 cannot
//
//	call one because it holds none — the guarantee is structural, not a
//	branch somebody could invert.
//
// SPORT: fleet.supervision.Tier3Supervisor/ADDED (P1-E18-W4-S39-T3).

package supervision

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// suggestionActor names tier 3 in the audit log.
const suggestionActor = "supervision.tier3"

// suggestionOutcome is the trace row's outcome for a filed suggestion.
const suggestionOutcome = "suggested"

// Suggestion is one proposed action, recorded for a human rather than run.
//
// It carries the action's REFERENCE and its rung, never the command text:
// a suggestion list an operator reads at leisure is exactly the wrong
// place to accumulate a second copy of every command the fleet wanted to
// run, and the routing record already binds the reference to the action.
type Suggestion struct {
	// Ref names the action, matching the attention item's SourceRef so the
	// two halves can be read together.
	Ref string `json:"ref"`
	// Verb is the action's verb, or empty when it is not a verb call.
	Verb string `json:"verb"`
	// Capability is the registered capability the action was evaluated
	// against.
	Capability string `json:"capability"`
	// RiskLevel is the rung the engine resolved. It is the engine's, not a
	// second classification — tier 3 classifies nothing (R-21.236).
	RiskLevel string `json:"risk_level"`
	// Rationale is the engine's own explanation for the verdict that put
	// the action here.
	Rationale string `json:"rationale"`
	// ParamsHash binds the suggestion to the exact parameters without
	// reproducing them.
	ParamsHash string `json:"params_hash"`
}

// Tier3Supervisor turns every action it is handed into a suggestion.
type Tier3Supervisor struct {
	// queue and trace may both be nil: a partially-wired daemon gets a
	// shorter trail, never a different outcome. Tier 3's outcome does not
	// depend on either.
	queue AttentionPusher
	trace audit.Writer
}

// NewTier3Supervisor builds the suggest-only supervisor.
//
// Neither collaborator is required, and that is deliberate rather than
// lax: the guarantee this tier makes is that the executor is NOT called,
// and that guarantee holds whether or not anybody is listening. A
// constructor that refused without a queue would make the safest tier the
// one most likely to fail to start.
func NewTier3Supervisor(queue AttentionPusher, trace audit.Writer) *Tier3Supervisor {
	return &Tier3Supervisor{queue: queue, trace: trace}
}

// Tier reports which tier this supervisor is, so a caller can log or
// display what actually handled an action.
func (s *Tier3Supervisor) Tier() Tier { return TierSuggestOnly }

// Suggest files req as a suggestion and returns it.
//
// It NEVER executes and holds nothing that could. The returned error
// reports only that the suggestion could not be FILED; it is not a verdict
// on the action, and a caller that ignores it has still not executed
// anything.
func (s *Tier3Supervisor) Suggest(ctx context.Context, req policy.EvalRequest, out policy.EvalOutcome) (Suggestion, error) {
	sg := Suggestion{
		Ref:        actionRef(req),
		Verb:       req.Verb,
		Capability: req.Capability,
		RiskLevel:  out.Level.String(),
		Rationale:  out.Reason,
		ParamsHash: audit.HashParams(req.Params),
	}
	s.emit(ctx, sg)
	if s.queue == nil {
		return sg, nil
	}
	if _, err := s.queue.Push(ctx, AttentionItem{
		Kind:      KindPolicyAsk,
		SourceRef: sg.Ref,
		ScopeRef:  ScopeRef{Kind: scope.ScopeKindGlobal},
		Priority:  autoAdvancePriorityAsk,
	}); err != nil {
		return sg, cascade.Wrap(cascade.KindUnavailable, err,
			"supervision: tier 3 could not file the suggestion; the action was NOT run")
	}
	return sg, nil
}

// emit writes the suggestion's detail to the append-only log. A failed
// append is ignored for the same reason dryrun_trace.go ignores one: the
// outcome is already fixed, and a logging failure must not be reported as
// though the action had been handled differently.
func (s *Tier3Supervisor) emit(ctx context.Context, sg Suggestion) {
	if s.trace == nil {
		return
	}
	payload, err := json.Marshal(sg)
	if err != nil {
		return
	}
	_, _ = s.trace.Append(ctx, audit.Event{
		Kind:       audit.KindPolicyRoute,
		Actor:      suggestionActor,
		Action:     sg.Ref,
		ParamsHash: sg.ParamsHash,
		RiskLevel:  sg.RiskLevel,
		Verdict:    policy.VerdictAsk.String(),
		Outcome:    suggestionOutcome,
		Explain:    payload,
	})
}
