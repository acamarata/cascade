// Purpose: contract:fanout-leg-results conductor acceptance (replay,
//   re-authorization, outcomes, collaborators, fan-out id separation);
//   the crash window and replay refusals live in fanout_crash_test.go.
// Constraints: records are kept as JSON bytes, so "byte-identical" is real.
// SPORT: conductor.fanout/CHANGE (tests) (P1-CORE-18).

package conductor

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// allowAll is the permissive AuthorizeFn double.
func allowAll(context.Context, provider.ModelRequest) error { return nil }

// memLegStore is an in-memory LegResultStore double (JSON per record);
// putErr/getErr inject a store failure.
type memLegStore struct {
	mu     sync.Mutex
	recs   map[string]string
	putErr error
	getErr error
}

func newMemLegStore() *memLegStore { return &memLegStore{recs: map[string]string{}} }

func (s *memLegStore) PutLegResult(_ context.Context, r LegResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putErr != nil {
		return s.putErr
	}
	key := LegResultKey(r.FanOutID, r.LegIndex)
	if _, ok := s.recs[key]; ok {
		return cascade.New(cascade.KindConflict, "memLegStore: exists")
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s.recs[key] = string(b)
	return nil
}

func (s *memLegStore) GetLegResult(_ context.Context, fanoutID string, legIndex int) (LegResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return LegResult{}, false, s.getErr
	}
	b, ok := s.recs[LegResultKey(fanoutID, legIndex)]
	if !ok {
		return LegResult{}, false, nil
	}
	var r LegResult
	err := json.Unmarshal([]byte(b), &r)
	return r, err == nil, err
}

// DeleteTask is unused by these tests (resume's adapter owns retention).
func (s *memLegStore) DeleteTask(context.Context, string) error { return nil }

func (s *memLegStore) snapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.recs)
}

// seedLegRecord stores leg idx's record as a completed run would have.
func seedLegRecord(t *testing.T, s *memLegStore, fanoutID string, req provider.ModelRequest, idx int, resp provider.ModelResponse) {
	t.Helper()
	legReq := req
	legReq.FanOut, legReq.ReservationID = 1, ""
	digest, err := legRequestDigest(fanoutID, idx, legReq)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	rec := LegResult{FanOutID: fanoutID, TaskID: req.TaskID, LegIndex: idx, Attempt: 1, RequestDigest: digest, Response: resp}
	if err := s.PutLegResult(context.Background(), rec); err != nil {
		t.Fatalf("seed record: %v", err)
	}
}

// attemptFor is spyJournal's attempt rule (caller holds s.mu).
func (s *spyJournal) attemptFor(kind, fanoutID string, legIndex int, fields map[string]string) uint64 {
	if kind != legKindStarted {
		a, _ := strconv.ParseUint(fields["attempt"], 10, 64)
		return a
	}
	if s.starts == nil {
		s.starts = map[string]uint64{}
	}
	key := LegResultKey(fanoutID, legIndex)
	s.starts[key]++
	return s.starts[key]
}

// completedOK is the ok-done leg map a resume scan would build.
func (s *spyJournal) completedOK(fanoutID string) map[int]JobID {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int]JobID{}
	for _, e := range s.entries {
		if e.kind == legKindDone && e.fanoutID == fanoutID && e.fields["outcome"] == LegOutcomeOK {
			out[e.legIndex] = JobID(e.fields["job_id"])
		}
	}
	return out
}

// outputExec is a counted exec double with distinct Output/Usage per call.
func outputExec(prefix string) (execFn, *int32) {
	var calls int32
	return func(_ context.Context, _ provider.ModelRequest) (provider.ModelResponse, error) {
		n := atomic.AddInt32(&calls, 1)
		return provider.ModelResponse{JobID: JobID(prefix + "-job-" + strconv.Itoa(int(n))), Output: prefix + "-out-" + strconv.Itoa(int(n)),
			Usage: provider.Usage{InputTokens: int(n), OutputTokens: 10 * int(n)}}, nil
	}, &calls
}

func TestFanOutReplayReturnsStoredContent(t *testing.T) {
	ctx := context.Background()
	store, j := newMemLegStore(), &spyJournal{}
	exec1, _ := outputExec("run1")
	first, err := FanOut(ctx, "fo-r", fanoutReq(), 3, nil, passthroughPermit, j, store, allowAll, exec1)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	exec2, calls2 := outputExec("run2")
	var authCalls int32
	auth := func(context.Context, provider.ModelRequest) error { atomic.AddInt32(&authCalls, 1); return nil }
	second, err := FanOut(ctx, "fo-r", fanoutReq(), 3, j.completedOK("fo-r"), passthroughPermit, j, store, auth, exec2)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if atomic.LoadInt32(calls2) != 0 || atomic.LoadInt32(&authCalls) != 3 {
		t.Fatalf("run 2: exec calls = %d (want 0), AuthorizeFn calls = %d (want exactly 1 per replayed leg, 3)", *calls2, authCalls)
	}
	for i := range first {
		if second[i].Output == "" || second[i].Output != first[i].Output || second[i].Usage != first[i].Usage || second[i].JobID != first[i].JobID {
			t.Fatalf("leg %d replay = %+v, want the stored %+v", i, second[i], first[i])
		}
	}
}

func TestFanOutReplayReauthorizes(t *testing.T) {
	ctx := context.Background()
	cfg, deps := newReadyConfig(t)
	policy := &fakePolicy{}
	cfg.Router, cfg.Policy = concurrencySafeRouter{}, policy
	var provCalls int32
	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		atomic.AddInt32(&provCalls, 1)
		return provider.ChatResponse{Message: provider.ChatMessage{Role: "assistant", Content: "sensitive-output"}}, nil
	}
	e, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	store, j := newMemLegStore(), &spyJournal{}
	if _, err := e.ExecuteFanOut(ctx, "fo-p", validReq(), 3, nil, passthroughPermit, j, store); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	before, audits := store.snapshot(), deps.audit.count()
	policy.err = errors.New("policy now denies this request")
	// Both replay paths: journaled done legs, and the crash window.
	for _, completed := range []map[int]JobID{j.completedOK("fo-p"), nil} {
		out, err := e.ExecuteFanOut(ctx, "fo-p", validReq(), 3, completed, passthroughPermit, j, store)
		if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), "policy refused") || out != nil {
			t.Fatalf("replay after the policy flip = (%v, %v), want the policy refusal and no output", out, err)
		}
	}
	if provCalls != 3 || len(before) != 3 || !maps.Equal(before, store.snapshot()) {
		t.Fatalf("provider calls = %d (want 3, all in run 1); records changed = %v", provCalls, !maps.Equal(before, store.snapshot()))
	}
	refusals := 0
	for _, ev := range deps.audit.events[audits:] {
		if ev.Verdict == "refusal" {
			refusals++
		}
	}
	if len(deps.audit.events)-audits != 6 || refusals != 6 {
		t.Fatalf("post-flip audit records = %d, refusals = %d, want 6 refusals (one per replayed leg, two runs)", len(deps.audit.events)-audits, refusals)
	}
}

func TestFanOutFailedLegOutcome(t *testing.T) {
	provErr := cascade.New(cascade.KindUnavailable, "provider down")
	policyErr := cascade.Wrap(cascade.KindPolicyDenied, errors.New("deny"), "conductor: policy refused")
	cases := []struct {
		name    string
		err     error
		permit  WithPermitFn
		outcome string
		wantErr error
	}{
		{"policy refusal", policyErr, passthroughPermit, LegOutcomeFailedTerminal, policyErr},
		{"sensitivity", ErrSensitivityViolation, passthroughPermit, LegOutcomeFailedTerminal, ErrSensitivityViolation},
		{"invalid input", ErrInvalidRequest, passthroughPermit, LegOutcomeFailedTerminal, ErrInvalidRequest},
		{"provider error", provErr, passthroughPermit, LegOutcomeFailedRetryable, provErr},
		{"permit denied", nil, func(context.Context, func(context.Context) error) error { return errors.New("no permit") }, LegOutcomeFailedRetryable, ErrAdmissionDenied},
		{"success", nil, passthroughPermit, LegOutcomeOK, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, j := newMemLegStore(), &spyJournal{}
			exec := func(context.Context, provider.ModelRequest) (provider.ModelResponse, error) {
				return provider.ModelResponse{JobID: "job-x", Output: "out"}, tc.err
			}
			_, err := FanOut(context.Background(), "fo-f", fanoutReq(), 1, nil, tc.permit, j, store, allowAll, exec)
			if err != tc.wantErr { // identity; nil for success
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if j.count(legKindDone) != 1 || j.entries[len(j.entries)-1].fields["outcome"] != tc.outcome {
				t.Fatalf("done entries = %+v, want exactly one with outcome %q", j.entries, tc.outcome)
			}
			_, found, _ := store.GetLegResult(context.Background(), "fo-f", 0)
			if _, done := j.completedOK("fo-f")[0]; found != (tc.outcome == LegOutcomeOK) || done != found {
				t.Fatalf("record stored = %v, counted completed = %v for outcome %q; want both only for ok", found, done, tc.outcome)
			}
		})
	}
}

func TestFanOutRequiresCollaborators(t *testing.T) {
	exec, calls := outputExec("x")
	j, s := &spyJournal{}, newMemLegStore()
	type args struct {
		id      string
		permit  WithPermitFn
		journal JournalAppender
		store   LegResultStore
		auth    AuthorizeFn
		exec    execFn
	}
	full := args{"fo", passthroughPermit, j, s, allowAll, exec}
	for name, mutate := range map[string]func(a *args){
		"nil store": func(a *args) { a.store = nil }, "nil authorize": func(a *args) { a.auth = nil },
		"nil permit": func(a *args) { a.permit = nil }, "nil journal": func(a *args) { a.journal = nil },
		"nil exec": func(a *args) { a.exec = nil }, "empty id": func(a *args) { a.id = "" },
	} {
		a := full
		mutate(&a)
		_, err := FanOut(context.Background(), a.id, fanoutReq(), 2, nil, a.permit, a.journal, a.store, a.auth, a.exec)
		if err != ErrConstructionFailed || err.Error() != ErrConstructionFailed.Error() {
			t.Errorf("%s: err = %v, want ErrConstructionFailed", name, err)
		}
	}
	e, _ := newReadyExecutor(t)
	if _, err := e.ExecuteFanOut(context.Background(), "fo", validReq(), 2, nil, passthroughPermit, j, nil); err != ErrConstructionFailed {
		t.Errorf("ExecuteFanOut(nil store): err = %v, want ErrConstructionFailed", err)
	}
	_, err := FanOut(context.Background(), "a#1", fanoutReq(), 2, nil, passthroughPermit, j, s, allowAll, exec)
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "reserved separator") {
		t.Errorf("fan-out id with '#': err = %v, want KindInvalidInput naming the separator", err)
	}
	if atomic.LoadInt32(calls) != 0 || len(j.entries) != 0 || len(s.recs) != 0 {
		t.Fatalf("dispatch happened before construction checks: exec=%d journal=%d records=%d", *calls, len(j.entries), len(s.recs))
	}
}

func TestFanOutSeparatesFanOutIDs(t *testing.T) {
	ctx := context.Background()
	store, j := newMemLegStore(), &spyJournal{}
	run := func(id, prefix string, completed map[int]JobID) []provider.ModelResponse {
		exec, _ := outputExec(prefix)
		out, err := FanOut(ctx, id, fanoutReq(), 2, completed, passthroughPermit, j, store, allowAll, exec)
		if err != nil {
			t.Fatalf("FanOut(%s): %v", id, err)
		}
		return out
	}
	a, b := run("fo-a", "A", nil), run("fo-b", "B", nil)
	if snap := store.snapshot(); len(snap) != 4 || snap["fo-a#1"] == "" || snap["fo-b#1"] == "" {
		t.Fatalf("records = %v, want fo-a#0..1 and fo-b#0..1", snap)
	}
	ra, _, _ := store.GetLegResult(ctx, "fo-a", 0)
	rb, _, _ := store.GetLegResult(ctx, "fo-b", 0)
	if ra.RequestDigest == rb.RequestDigest || ra.TaskID != fanoutReq().TaskID || rb.TaskID != fanoutReq().TaskID {
		t.Fatalf("want distinct digests and the client TaskID on both legs: a=%+v b=%+v", ra, rb)
	}
	if j.starts["fo-a#0"] != 1 || j.starts["fo-b#0"] != 1 {
		t.Fatalf("per-fan-out starts = %v, want one start per leg per fan-out", j.starts)
	}
	ra2, rb2 := run("fo-a", "Z", j.completedOK("fo-a")), run("fo-b", "Z", j.completedOK("fo-b"))
	if ra2[0].Output != a[0].Output || rb2[0].Output != b[0].Output || a[0].Output == b[0].Output {
		t.Fatalf("replays crossed fan-outs: a=%v/%v b=%v/%v", a[0].Output, ra2[0].Output, b[0].Output, rb2[0].Output)
	}
}
