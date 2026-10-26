package conversation

// Purpose: the new-session flow over a real *rpc.Registry, a real Resumer
//   and a real store: chat.create_thread(tier) makes the session thread,
//   chat.resume composes INTO it (its stored tier is the target), and the
//   next chat.append_turn stores the block as segment 0 of that turn.
// SPORT: internal.conversation.resume/ADDED (tests) (P1-E22-W5-S47-T2).

import (
	"context"
	"testing"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// newFreshSessionTestAdapter wires a REAL Resumer (real Composer, real
// store, a recording passthrough substitutor) onto a real registry, so a
// test proves the admission verdict, not only a wire round trip.
func newFreshSessionTestAdapter(t *testing.T) (*Adapter, *rpc.Registry, Store) {
	t.Helper()
	store := newTestStore(t)
	adapter := NewAdapter(store, &fakeBus{}, passthroughSubst{}, newAdapterTestClock(), "")
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)
	composer, err := cascadecontext.NewComposer(provider.NaiveTokenCounter{}, nil)
	if err != nil {
		t.Fatalf("NewComposer: %v", err)
	}
	resumeSvc, err := NewResumer(store, composer, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("NewResumer: %v", err)
	}
	adapter.SetResume(resumeSvc)
	return adapter, registry, store
}

// TestAppendTurnLeadingContextIsSegmentZero (contract name): the block a
// resume returns, sent as leading_context on the session's next append, is
// stored as segment 0 (SegmentText) ahead of the caller's own segment, on
// the session thread create_thread made. An append without it stores only
// the caller's segment. Failing input: appending the block after the
// caller's segments, or dropping it.
func TestAppendTurnLeadingContextIsSegmentZero(t *testing.T) {
	_, registry, store := newFreshSessionTestAdapter(t)
	topicID := seedMarkedThread(t, registry, "restricted", "hello there")
	session := seedMarkedThread(t, registry, "restricted", "")

	res, errObj := dispatch(t, registry, MethodResume, resumeParams{TopicID: topicID, SessionThreadID: session})
	if errObj != nil {
		t.Fatalf("chat.resume: %+v", errObj)
	}
	block := res.(resumeResult).LeadingContext
	if block == "" {
		t.Fatal("chat.resume returned an empty leading_context")
	}
	segs := appendAndList(t, registry, store, appendTurnParams{
		ThreadID: session, Role: "user", LeadingContext: block,
		Segments: []appendSegmentWire{{Kind: "text", Content: "my next prompt"}},
	})
	if len(segs) != 2 || segs[0].Seq != 0 || segs[0].Kind != SegmentText || segs[0].Content != block {
		t.Fatalf("segments = %+v, want segment 0 = the resumed block", segs)
	}
	if segs[1].Seq != 1 || segs[1].Content != "my next prompt" {
		t.Fatalf("segment 1 = %+v, want the caller's prompt", segs[1])
	}
	plain := appendAndList(t, registry, store, appendTurnParams{
		ThreadID: session, Role: "user", Segments: []appendSegmentWire{{Kind: "text", Content: "later"}},
	})
	if len(plain) != 1 || plain[0].Content != "later" {
		t.Fatalf("a plain append stored %+v, want only its own segment", plain)
	}
}

// appendAndList appends params and returns the stored segments of the
// turn it created.
func appendAndList(t *testing.T, registry *rpc.Registry, store Store, params appendTurnParams) []Segment {
	t.Helper()
	res, errObj := dispatch(t, registry, MethodAppendTurn, params)
	if errObj != nil {
		t.Fatalf("chat.append_turn: %+v", errObj)
	}
	appended := res.(appendTurnResult)
	if appended.ThreadID != params.ThreadID {
		t.Fatalf("append landed on %q, want %q", appended.ThreadID, params.ThreadID)
	}
	segs, err := store.ListSegments(context.Background(), appended.TurnID)
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	return segs
}

// TestResumeNewSession_CreatedTierIsTheTarget proves the tier create_thread
// stored is what admission compares against: a restricted source is
// admitted into a local-only session and refused from a local-only source
// into a restricted one.
func TestResumeNewSession_CreatedTierIsTheTarget(t *testing.T) {
	_, registry, store := newFreshSessionTestAdapter(t)
	restrictedSrc := seedMarkedThread(t, registry, "restricted", "restricted content")
	localSrc := seedMarkedThread(t, registry, "local-only", "local content")
	localSession := seedMarkedThread(t, registry, "local-only", "")
	restrictedSession := seedMarkedThread(t, registry, "restricted", "")
	if got, _ := store.ThreadPrivacy(context.Background(), localSession); got != provider.SensitivityLocalOnly {
		t.Fatalf("session tier = %s, want local-only", got)
	}
	if _, errObj := dispatch(t, registry, MethodResume, resumeParams{TopicID: restrictedSrc, SessionThreadID: localSession}); errObj != nil {
		t.Fatalf("restricted into local-only session refused: %+v", errObj)
	}
	_, errObj := dispatch(t, registry, MethodResume, resumeParams{TopicID: localSrc, SessionThreadID: restrictedSession})
	if errObj == nil || errObj.Code != cascade.RPCCodePolicyDenied {
		t.Fatalf("local-only into restricted session = %+v, want RPCCodePolicyDenied", errObj)
	}
}
