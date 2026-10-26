package conversation

// Purpose: the source-versus-target admission rule through the REAL
//   chat.resume wire path: the 16-cell table (TestResume, literal
//   expectations taken from the contract's strict order, never computed by
//   the code under test) and the contract's named refusal/admission cells.
//   Every thread is made the way production makes it: chat.create_thread
//   with a required tier, then chat.append_turn.
// SPORT: internal.conversation.resume/ADDED (tests) (P1-E22-W5-S47-T2).

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// seedMarkedThread creates a thread at tier over the wire and appends one
// turn of content to it, returning its id.
func seedMarkedThread(t *testing.T, registry *rpc.Registry, tier, content string) string {
	t.Helper()
	created, errObj := dispatch(t, registry, MethodCreateThread, createThreadParams{PrivacyMode: tier})
	if errObj != nil {
		t.Fatalf("create_thread(%s): %+v", tier, errObj)
	}
	id := created.(createThreadResult).ThreadID
	if content != "" {
		if _, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
			ThreadID: id, Role: "user", Segments: []appendSegmentWire{{Kind: "text", Content: content}},
		}); errObj != nil {
			t.Fatalf("append_turn(%s): %+v", id, errObj)
		}
	}
	return id
}

// resumeCell resumes a fresh source thread at source into a fresh
// destination thread at target and returns the wire error, if any.
func resumeCell(t *testing.T, source, target string) *rpc.ErrorObject {
	t.Helper()
	_, registry, _ := newFreshSessionTestAdapter(t)
	topicID := seedMarkedThread(t, registry, source, "cell body from a "+source+" thread")
	destID := seedMarkedThread(t, registry, target, "")
	_, errObj := dispatch(t, registry, MethodResume, resumeParams{TopicID: topicID, SessionThreadID: destID})
	return errObj
}

// TestResume is the 16-cell source x target table. The expectation column
// is written out by hand from the contract (local-only > restricted >
// internal > public; refuse exactly when the source is more restrictive):
// six refusals, ten admissions.
func TestResume(t *testing.T) {
	cells := []struct {
		source, target string
		admit          bool
	}{
		{"local-only", "local-only", true},
		{"local-only", "restricted", false},
		{"local-only", "internal", false},
		{"local-only", "public", false},
		{"restricted", "local-only", true},
		{"restricted", "restricted", true},
		{"restricted", "internal", false},
		{"restricted", "public", false},
		{"internal", "local-only", true},
		{"internal", "restricted", true},
		{"internal", "internal", true},
		{"internal", "public", false},
		{"public", "local-only", true},
		{"public", "restricted", true},
		{"public", "internal", true},
		{"public", "public", true},
	}
	for _, c := range cells {
		t.Run(c.source+"_into_"+c.target, func(t *testing.T) {
			errObj := resumeCell(t, c.source, c.target)
			if c.admit && errObj != nil {
				t.Fatalf("refused, want admitted: %+v", errObj)
			}
			if !c.admit && (errObj == nil || errObj.Code != cascade.RPCCodePolicyDenied) {
				t.Fatalf("errObj = %+v, want RPCCodePolicyDenied", errObj)
			}
		})
	}
}

// TestResumeRefusesRestrictedIntoPublic (contract name): a restricted
// source into a public session is refused with a message naming both tiers.
func TestResumeRefusesRestrictedIntoPublic(t *testing.T) {
	assertPrivacyRefusal(t, resumeCell(t, "restricted", "public"), "restricted", "public")
}

// TestResumeRefusesRestrictedIntoInternal (contract name): restricted
// content never enters an internal thread, whose egress classes (the
// Telegram bridge admits internal) would carry it off the machine. Failing
// input: a lane-count comparison, under which restricted equals internal.
func TestResumeRefusesRestrictedIntoInternal(t *testing.T) {
	assertPrivacyRefusal(t, resumeCell(t, "restricted", "internal"), "restricted", "internal")
}

// TestResumeRefusesInternalIntoPublic (contract name): an internal source
// into a public session is refused with a message naming both tiers.
func TestResumeRefusesInternalIntoPublic(t *testing.T) {
	assertPrivacyRefusal(t, resumeCell(t, "internal", "public"), "internal", "public")
}

// TestResumeInternalIntoRestrictedAllowed (contract name): a less
// restrictive source into a stricter session is admitted and its content
// comes back as the leading context.
func TestResumeInternalIntoRestrictedAllowed(t *testing.T) {
	_, registry, _ := newFreshSessionTestAdapter(t)
	topicID := seedMarkedThread(t, registry, "internal", "internal notes")
	destID := seedMarkedThread(t, registry, "restricted", "")
	res, errObj := dispatch(t, registry, MethodResume, resumeParams{TopicID: topicID, SessionThreadID: destID})
	if errObj != nil {
		t.Fatalf("internal into restricted refused: %+v", errObj)
	}
	got := res.(resumeResult)
	if got.ThreadID != topicID || !strings.Contains(got.LeadingContext, "internal notes") {
		t.Fatalf("result = %+v, want the internal thread's content", got)
	}
}

// assertPrivacyRefusal checks a wire refusal is KindPolicyDenied and that
// its message names the source and destination tiers.
func assertPrivacyRefusal(t *testing.T, errObj *rpc.ErrorObject, source, target string) {
	t.Helper()
	if errObj == nil {
		t.Fatalf("%s into %s was admitted, want refused", source, target)
	}
	if errObj.Code != cascade.RPCCodePolicyDenied {
		t.Fatalf("errObj.Code = %d, want RPCCodePolicyDenied", errObj.Code)
	}
	if !strings.Contains(errObj.Message, "is "+source) || !strings.Contains(errObj.Message, target) {
		t.Fatalf("refusal %q does not name both tiers", errObj.Message)
	}
}
