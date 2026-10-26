package conversation

// Purpose: core unit tests for resume.go -- construction, request
//   validation, orchestration (which path runs, budget default,
//   substitution ALWAYS runs), and idempotency. resume_topic_test.go and
//   resume_query_test.go cover their own files' specifics; split under
//   Art.10.3's 300-line cap, matching production's own resume.go/
//   resume_topic.go/resume_query.go split.
// SPORT: internal.conversation.resume/ADDED (tests) (P1-E22-W5-S47-T2).

import (
	"context"
	"errors"
	"strings"
	"testing"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeComposer records every Compose call and returns a pre-set result or
// error, so orchestration tests never depend on a real Composer's
// token-counting.
type fakeComposer struct {
	gotBudget int
	gotSlots  []cascadecontext.Slot
	result    cascadecontext.ComposedResult
	err       error
}

func (f *fakeComposer) Compose(_ context.Context, budget int, slots []cascadecontext.Slot) (cascadecontext.ComposedResult, error) {
	f.gotBudget = budget
	f.gotSlots = slots
	if f.err != nil {
		return cascadecontext.ComposedResult{}, f.err
	}
	return f.result, nil
}

// echoComposer is a real-shaped, deterministic Composer double: every
// slot survives whole (no trimming), TokensUsed is the slot count, so
// orchestration tests can assert on Context content without a real
// TokenCounter.
type echoComposer struct{}

func (echoComposer) Compose(_ context.Context, budget int, slots []cascadecontext.Slot) (cascadecontext.ComposedResult, error) {
	out := cascadecontext.ComposedResult{Budget: budget, TokensUsed: len(slots)}
	for _, s := range slots {
		out.Slots = append(out.Slots, cascadecontext.ComposedSlot{Kind: s.Kind, Label: s.Label, Content: s.Content, Tokens: 1})
	}
	return out, nil
}

// recordingSubst records every Substitute call's input -- including the
// tier it was called at -- and returns the
// content unchanged, unless failWith is set.
type recordingSubst struct {
	got      []byte
	gotTier  egress.SensitivityTier
	failWith error
}

func (r *recordingSubst) InterceptClass(_ context.Context, _ egress.EgressClass, tier egress.SensitivityTier, content []byte) ([]byte, error) {
	r.got = content
	r.gotTier = tier
	if r.failWith != nil {
		return nil, r.failWith
	}
	return content, nil
}

func newResumeStore(t *testing.T) Store {
	t.Helper()
	return newTestStore(t)
}

// TestResume (the 16-cell table) lives in resume_matrix_test.go.

// TestNewResumer_RequiredDeps proves store/composer are
// required; querier/subst are not.
func TestNewResumer_RequiredDeps(t *testing.T) {
	store := newResumeStore(t)
	composer := &fakeComposer{}
	if _, err := NewResumer(nil, composer, nil, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil store: got %v, want KindInvalidInput", err)
	}
	if _, err := NewResumer(store, nil, nil, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil composer: got %v, want KindInvalidInput", err)
	}
	if _, err := NewResumer(store, composer, nil, nil); err != nil {
		t.Fatalf("nil querier/subst must be allowed at construction: %v", err)
	}
}

// TestResume_ValidateRequest_ExactlyOneSource: the failing inputs are
// "neither" and "both" -- both must be refused with ErrResumeInvalidRequest.
func TestResume_ValidateRequest_ExactlyOneSource(t *testing.T) {
	store := newResumeStore(t)
	r, err := NewResumer(store, &fakeComposer{}, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	cases := []ResumeRequest{
		{},                           // neither
		{TopicID: "t1", Query: "q1"}, // both
	}
	for _, req := range cases {
		if _, err := r.Resume(context.Background(), req); !errors.Is(err, ErrResumeInvalidRequest) || !strings.Contains(err.Error(), ErrResumeInvalidRequest.Error()) {
			t.Errorf("Resume(%+v) = %v, want ErrResumeInvalidRequest", req, err)
		}
	}
}

// TestResume_DefaultBudget: Budget<=0 substitutes DefaultResumeBudget --
// the failing input is Budget: 0, which Compose itself would refuse
// outright (composer_core.go) if passed through unchanged.
func TestResume_DefaultBudget(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThread(t, store, "hello")
	fc := &fakeComposer{result: cascadecontext.ComposedResult{Budget: DefaultResumeBudget, TokensUsed: 1}}
	r, err := NewResumer(store, fc, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if _, err := r.Resume(context.Background(), ResumeRequest{TopicID: threadID, Budget: 0}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if fc.gotBudget != DefaultResumeBudget {
		t.Errorf("Compose called with budget %d, want DefaultResumeBudget %d", fc.gotBudget, DefaultResumeBudget)
	}
}

// TestResume_ComposeFailure_Propagates: the failing input is a Composer
// that errors (composer_errors.go's own shape); Resume must wrap it,
// never swallow it.
func TestResume_ComposeFailure_Propagates(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThread(t, store, "hello")
	fc := &fakeComposer{err: cascade.New(cascade.KindInvalidInput, "composer: boom")}
	r, err := NewResumer(store, fc, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	_, err = r.Resume(context.Background(), ResumeRequest{TopicID: threadID})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Resume with failing composer = %v, want KindUnavailable", err)
	}
}

// TestResume_SubstitutionAlwaysRuns proves the composed text ALWAYS
// transits substitution: recordingSubst records what it received, and
// this asserts it received the joined slot content, not something else
// (e.g. an empty string from a code path that forgot to call it).
func TestResume_SubstitutionAlwaysRuns(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThread(t, store, "the-secret-marker")
	sub := &recordingSubst{}
	r, err := NewResumer(store, echoComposer{}, nil, sub)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	res, err := r.Resume(context.Background(), ResumeRequest{TopicID: threadID})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if !strings.Contains(string(sub.got), "the-secret-marker") {
		t.Fatalf("substitutor received %q, want it to contain the composed slot content", string(sub.got))
	}
	if res.Context != string(sub.got) {
		t.Fatalf("Resume returned Context %q, want the substitutor's (passthrough) output %q", res.Context, string(sub.got))
	}
}

// TestResume_SubstitutionFailure_NoContentLeak: the failing input is a
// substitutor that errors; Resume's own returned error must never
// contain the seeded content, matching sse.go's identical proof.
func TestResume_SubstitutionFailure_NoContentLeak(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThread(t, store, "must-not-leak-into-error")
	sub := &recordingSubst{failWith: errors.New("substitution transport: connection reset by must-not-leak-into-error")}
	r, err := NewResumer(store, echoComposer{}, nil, sub)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	_, err = r.Resume(context.Background(), ResumeRequest{TopicID: threadID})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Resume with failing substitutor = %v, want KindUnavailable", err)
	}
	if strings.Contains(err.Error(), "must-not-leak-into-error") {
		t.Fatalf("Resume's error leaked seeded content: %v", err)
	}
}

// TestResume_SubstitutionUsesSourceThreadsRealTier proves resume_egress.go
// sends the SOURCE thread's own §5.16 tier to InterceptClass, not a fixed
// one: the failing input is a hardcoded "internal", which this public
// thread's assertion catches.
func TestResume_SubstitutionUsesSourceThreadsRealTier(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThreadAt(t, store, provider.SensitivityPublic, "hello")
	sub := &recordingSubst{}
	r, err := NewResumer(store, echoComposer{}, nil, sub)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if _, err := r.Resume(context.Background(), ResumeRequest{TopicID: threadID, IntoTier: provider.SensitivityPublic}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if sub.gotTier != provider.SensitivityPublic {
		t.Fatalf("InterceptClass tier = %q, want %q (the thread's own stored tier)", sub.gotTier, "public")
	}
}

// TestResumeSubstitutionFailureOpensNoSession (contract name): a
// substitutor failure must return a ZERO ResumeResult
// (Context=="", ThreadID=="") alongside the error -- "opens no session"
// means the caller has nothing usable to inject, not merely that an
// error is also present. Complements TestResume_SubstitutionFailure_NoContentLeak,
// which proves the ERROR itself never carries the leaked content; this
// test proves the RESULT never carries anything either.
func TestResumeSubstitutionFailureOpensNoSession(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThread(t, store, "must not open a session")
	sub := &recordingSubst{failWith: errors.New("substitution transport: connection reset")}
	r, err := NewResumer(store, echoComposer{}, nil, sub)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	res, err := r.Resume(context.Background(), ResumeRequest{TopicID: threadID})
	if err == nil {
		t.Fatal("Resume with a failing substitutor returned nil error")
	}
	if res.Context != "" || res.ThreadID != "" || res.TokensUsed != 0 {
		t.Fatalf("Resume returned a non-zero result %+v alongside the error; a substitution failure must open no session", res)
	}
}

// TestResume_NilSubstitutor_FailsClosed proves the ticket's own PRIVACY
// requirement ("no vaulted value may appear in the injected context
// block"): a resume with no substitutor configured never returns
// unsubstituted content, unlike sse.go's Substitutor (nil there is a
// documented passthrough for a DIFFERENT, local-echo seam).
func TestResume_NilSubstitutor_FailsClosed(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThread(t, store, "hello")
	r, err := NewResumer(store, echoComposer{}, nil, nil)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	_, err = r.Resume(context.Background(), ResumeRequest{TopicID: threadID})
	if !errors.Is(err, ErrResumeSubstitutionUnavailable) || !strings.Contains(err.Error(), ErrResumeSubstitutionUnavailable.Error()) {
		t.Fatalf("Resume with nil substitutor = %v, want ErrResumeSubstitutionUnavailable", err)
	}
}
