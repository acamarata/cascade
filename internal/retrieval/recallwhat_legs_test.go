package retrieval

// Purpose: TestConversationOutcome_AlwaysUnavailable and
// TestConversationOutcome_NilLegNotConfigured, split out of
// recallwhat_test.go purely for the 300-line cap -- the exact-Kind proof
// for conversationOutcome's D6/Q1 unconditional exclusion (recallwhat_legs.go).
//
// SPORT: internal.retrieval.RecallWhatService/ADDED (P1-E22-W5-S47-T1).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestConversationOutcome_AlwaysUnavailable is D6/Q1's exact-Kind proof at
// the leg level (Response.Errors is already a stringified message by the
// time Query assembles it, so the precise cascade.Kind is only checkable
// here, directly against conversationOutcome's own return): a configured
// conversation leg reports KindUnavailable on EVERY call, with no
// candidates, and SearchTurns is never reached (baselineLegs' conv.matches
// is non-empty, yet zero hits survive -- proving the leg does not even try
// rather than trying and failing).
func TestConversationOutcome_AlwaysUnavailable(t *testing.T) {
	_, conv, _ := baselineLegs() // conv.matches is non-empty: SearchTurns is simply never called
	svc := newBaselineService(t, nil, conv, nil)
	out := svc.conversationOutcome(context.Background(), RecallWhatRequest{Query: "hello", Scope: "proj1"}, 10)
	if !out.configured {
		t.Fatal("conversationOutcome reported not-configured despite a real leg injected")
	}
	kind, _ := cascade.KindOf(out.err)
	if out.err == nil || kind != cascade.KindUnavailable {
		t.Fatalf("conversationOutcome err = %v (kind %v), want KindUnavailable", out.err, kind)
	}
	if len(out.list.Hits) != 0 {
		t.Fatalf("conversationOutcome returned candidates despite being unavailable: %+v", out.list.Hits)
	}
}

// TestConversationOutcome_NilLegNotConfigured: a build with no
// conversation store wired reports "not configured", not an error -- the
// two are different signals (absent vs. unconditionally refused). Built
// via NewRecallWhatService directly, not newBaselineService: that
// helper's conv parameter is the CONCRETE *fakeConvLeg type, so passing
// it a literal nil there would box a typed-nil pointer into the
// RecallWhatConversationLeg interface field -- a non-nil interface with a
// nil value underneath, the classic Go gotcha, and NOT what "no
// conversation store wired" means in production (daemon_unix_recall_what.go
// always assigns a real conversation.Store; the unassigned-interface
// pattern is the memory leg's). Passing nil directly
// to this interface-typed parameter is the only way to get a genuinely
// nil interface.
func TestConversationOutcome_NilLegNotConfigured(t *testing.T) {
	files, _, mem := baselineLegs()
	svc, err := NewRecallWhatService(files, nil, mem, rrf.Params{}, testClock(), resolverFor("proj1"), testEgress(t))
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	out := svc.conversationOutcome(context.Background(), RecallWhatRequest{Query: "hello", Scope: "proj1"}, 10)
	if out.configured {
		t.Fatal("a nil conversation leg reported configured")
	}
	if out.err != nil {
		t.Fatalf("a nil conversation leg reported an error: %v", out.err)
	}
}
