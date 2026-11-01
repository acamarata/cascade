package conductor

// Purpose (this file): transient retry through the real Executor - the
//   per-lane bound, full-jitter delays from a seeded source, the deadline
//   guard, the single spill, the refusal set that is never retried, and
//   the attempts the audit record and usage row carry.
// SPORT: conductor.transient-retry (TEST) - P1-GWY-03.

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// retryClock is an injected clock the recording sleeper advances.
type retryClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *retryClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// sleepRecorder is a Sleeper that never blocks: it records d, advances the
// clock by d, and reports the clock time each pause began.
type sleepRecorder struct {
	clock  *retryClock
	mu     sync.Mutex
	delays []time.Duration
	starts []time.Time
}

func (s *sleepRecorder) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock.mu.Lock()
	s.starts = append(s.starts, s.clock.now)
	s.clock.now = s.clock.now.Add(d)
	s.clock.mu.Unlock()
	s.delays = append(s.delays, d)
	return ctx.Err()
}

// retryRig is failoverRig with the retry policy p installed and per-lane
// call counters on Chat. fail answers lane-1 and lane-2 by attempt.
func retryRig(t *testing.T, p RetryPolicy, eligible LaneEligible, fail map[string]func(n int) error) (*Executor, *readyDeps, *recUsage, *sleepRecorder, map[string]int) {
	t.Helper()
	exec, deps, usage, provs := failoverRig(t)
	clock := &retryClock{now: time.Now()}
	rec := &sleepRecorder{clock: clock}
	exec.SetRetry(p, clock, rec.sleep, rand.New(rand.NewPCG(1, 2)), eligible)
	calls := map[string]int{}
	var mu sync.Mutex
	for lane, prov := range provs {
		prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
			mu.Lock()
			calls[lane]++
			n := calls[lane]
			mu.Unlock()
			if f := fail[lane]; f != nil {
				if err := f(n); err != nil {
					return provider.ChatResponse{}, err
				}
			}
			return provider.ChatResponse{Message: provider.ChatMessage{Role: "assistant", Content: "ok"}, Usage: provider.Usage{InputTokens: 2, OutputTokens: 1}}, nil
		}
	}
	return exec, deps, usage, rec, calls
}

// transient503 is a fresh marked 503 per call, so identity names the attempt.
func transient503(retryAfter time.Duration) func(int) error {
	return func(int) error {
		return provider.MarkTransient(cascade.New(cascade.KindUnavailable, "upstream: http 503"),
			provider.TransientInfo{Cause: provider.TransientHTTP5xx, Status: 503, RetryAfter: retryAfter})
	}
}

// goldenDelays are the full-jitter draws of rand.NewPCG(1, 2) under
// DefaultRetryPolicy for attempts 1 and 2 (ceilings 500ms and 1s).
var goldenDelays = []time.Duration{384686635, 616436224}

func TestRetryBoundedWithinDeadline(t *testing.T) {
	p := DefaultRetryPolicy()
	exec, deps, _, rec, calls := retryRig(t, p, nil, map[string]func(int) error{"lane-1": transient503(0)})
	if _, err := exec.Execute(context.Background(), validReq()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls["lane-1"] != 3 || calls["lane-2"] != 1 || deps.router.calls != 2 {
		t.Fatalf("lane-1 %d, lane-2 %d, Select %d; want 3, 1, 2", calls["lane-1"], calls["lane-2"], deps.router.calls)
	}
	if len(rec.delays) != len(goldenDelays) {
		t.Fatalf("delays = %v, want %v", rec.delays, goldenDelays)
	}
	for i, d := range rec.delays {
		ceiling := min(p.Cap, p.Base<<i)
		if d != goldenDelays[i] || d < 0 || d > ceiling || d > p.Cap {
			t.Fatalf("delay %d = %d, want golden %v within [0, %v]", i, int64(d), goldenDelays[i], ceiling)
		}
	}

	// A Retry-After the deadline cannot cover: no pause at all, one spill.
	exec, deps, _, rec, calls = retryRig(t, p, nil, map[string]func(int) error{
		"lane-1": transient503(2 * time.Second), "lane-2": transient503(2 * time.Second)})
	ctx, cancel := context.WithDeadline(context.Background(), rec.clock.Now().Add(time.Second))
	defer cancel()
	if _, err := exec.Execute(ctx, validReq()); err == nil {
		t.Fatal("Execute succeeded, want the last transient error")
	}
	if len(rec.delays) != 0 || calls["lane-1"] != 1 || calls["lane-2"] != 1 || deps.router.calls != 2 {
		t.Fatalf("delays %v, lane-1 %d, lane-2 %d, Select %d; want no pause, 1, 1, 2", rec.delays, calls["lane-1"], calls["lane-2"], deps.router.calls)
	}

	// Growing delays inside a 1s deadline: every pause ends by the deadline.
	tight := RetryPolicy{MaxAttempts: 3, Base: 600 * time.Millisecond, Cap: 8 * time.Second}
	exec, _, _, rec, _ = retryRig(t, tight, nil, map[string]func(int) error{"lane-1": transient503(0), "lane-2": transient503(0)})
	deadline := rec.clock.Now().Add(time.Second)
	ctx2, cancel2 := context.WithDeadline(context.Background(), deadline)
	defer cancel2()
	_, _ = exec.Execute(ctx2, validReq())
	if len(rec.delays) == 0 || len(rec.delays) >= 4 {
		t.Fatalf("pauses = %v, want at least one, and fewer than the 4 an unbounded call takes", rec.delays)
	}
	for i, d := range rec.delays {
		if end := rec.starts[i].Add(d); end.After(deadline) || d > tight.Cap {
			t.Fatalf("pause %d of %v ends at %v, past the deadline %v", i, d, end, deadline)
		}
	}
}

func TestRetryNeverOnNonTransient(t *testing.T) {
	marked := func(k cascade.Kind, status int) func(int) error {
		return func(int) error {
			return provider.MarkTransient(cascade.New(k, "refused"), provider.TransientInfo{Cause: provider.TransientHTTP5xx, Status: status})
		}
	}
	cases := map[string]func(int) error{
		"429":             marked(cascade.KindQuotaExhausted, 429),
		"429 unavailable": marked(cascade.KindUnavailable, 429),
		"401":             marked(cascade.KindPermissionDenied, 401),
		"policy-denied":   marked(cascade.KindPolicyDenied, 0),
		"canceled":        marked(cascade.KindCanceled, 0),
		"helper failure":  func(int) error { return cascade.New(cascade.KindUnavailable, "credential helper exited 1") },
	}
	for name, fail := range cases {
		exec, deps, _, rec, calls := retryRig(t, DefaultRetryPolicy(), nil, map[string]func(int) error{"lane-1": fail})
		_, err := exec.Execute(context.Background(), validReq())
		if err == nil || calls["lane-1"] != 1 || calls["lane-2"] != 0 || deps.router.calls != 1 || len(rec.delays) != 0 {
			t.Fatalf("%s: err %v, lane-1 %d, lane-2 %d, Select %d, pauses %v; want one attempt, no spill",
				name, err, calls["lane-1"], calls["lane-2"], deps.router.calls, rec.delays)
		}
	}
	denied := func(int) error { return errDenied }
	exec, deps, _, rec, calls := retryRig(t, DefaultRetryPolicy(), nil, map[string]func(int) error{"lane-1": denied, "lane-2": denied})
	_, err := exec.Execute(context.Background(), validReq())
	if !cascade.HasKind(err, cascade.KindCapabilityDenied) || err.Error() != errDenied.Error() {
		t.Fatalf("capability-denied: err = %v, want %v", err, errDenied)
	}
	if calls["lane-1"] != 1 || calls["lane-2"] != 1 || deps.router.calls != 2 || len(rec.delays) != 0 {
		t.Fatalf("capability-denied: lane-1 %d, lane-2 %d, Select %d; want its single failover (1, 1, 2)", calls["lane-1"], calls["lane-2"], deps.router.calls)
	}
}

// lastTrace decodes the last audit record's Explain.
func lastTrace(t *testing.T, deps *readyDeps) retryTrace {
	t.Helper()
	deps.audit.mu.Lock()
	defer deps.audit.mu.Unlock()
	var tr retryTrace
	if err := json.Unmarshal(deps.audit.events[len(deps.audit.events)-1].Explain, &tr); err != nil {
		t.Fatalf("decoding audit explain: %v", err)
	}
	return tr
}

func TestSpillOnceAfterTransientExhaustion(t *testing.T) {
	exec, deps, usage, _, _ := retryRig(t, DefaultRetryPolicy(), nil, map[string]func(int) error{"lane-1": transient503(0)})
	if _, err := exec.Execute(context.Background(), validReq()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	final := usage.recs[len(usage.recs)-1]
	if final.Attempt != 4 || final.LaneID != "lane-2" || len(usage.recs) != 4 {
		t.Fatalf("usage = %+v, want 4 rows ending with attempt 4 on lane-2", usage.recs)
	}
	if tr := lastTrace(t, deps); deps.audit.count() != 1 || tr.Attempts != 4 || !tr.Transient || tr.Outcome != "success" {
		t.Fatalf("audit = %d records, last %+v; want one success with attempts 4 transient true", deps.audit.count(), tr)
	}

	var lastErr error
	lane2 := func(int) error { lastErr = transient503(0)(0); return lastErr }
	exec, deps, _, _, calls := retryRig(t, DefaultRetryPolicy(), nil, map[string]func(int) error{"lane-1": transient503(0), "lane-2": lane2})
	_, err := exec.Execute(context.Background(), validReq())
	if err != lastErr || err.Error() != "unavailable: upstream: http 503" {
		t.Fatalf("err = %v, want lane-2's last error by identity", err)
	}
	if calls["lane-1"] != 3 || calls["lane-2"] != 3 || deps.router.calls != 2 {
		t.Fatalf("lane-1 %d, lane-2 %d, Select %d; want 3, 3, 2 (no third lane)", calls["lane-1"], calls["lane-2"], deps.router.calls)
	}
	if tr := lastTrace(t, deps); tr.Attempts != 6 || !tr.Transient || tr.Outcome != "provider_failure" {
		t.Fatalf("audit = %+v, want provider_failure with attempts 6 transient true", tr)
	}
}

func TestRetryHardBoundSixAttempts(t *testing.T) {
	always := transient503(0)
	exec, deps, _, _, calls := retryRig(t, DefaultRetryPolicy(), nil, map[string]func(int) error{"lane-1": always, "lane-2": always})
	if _, err := exec.Execute(context.Background(), validReq()); err == nil {
		t.Fatal("Execute succeeded, want the exhausted transient error")
	}
	if calls["lane-1"]+calls["lane-2"] != 6 || deps.router.calls != 2 {
		t.Fatalf("attempts %d (lane-1 %d, lane-2 %d), Select %d; want 6 and 2", calls["lane-1"]+calls["lane-2"], calls["lane-1"], calls["lane-2"], deps.router.calls)
	}
}

func TestRetryAfterBeyondCapSpills(t *testing.T) {
	exec, deps, _, rec, calls := retryRig(t, DefaultRetryPolicy(), nil, map[string]func(int) error{"lane-1": transient503(60 * time.Second)})
	if _, err := exec.Execute(context.Background(), validReq()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(rec.delays) != 0 || calls["lane-1"] != 1 || calls["lane-2"] != 1 || deps.router.calls != 2 {
		t.Fatalf("pauses %v, lane-1 %d, lane-2 %d, Select %d; want no pause and a spill", rec.delays, calls["lane-1"], calls["lane-2"], deps.router.calls)
	}
}

func TestRetryStopsWhenLaneIneligible(t *testing.T) {
	var asked []LaneID
	eligible := func(_ context.Context, lane LaneID) bool {
		asked = append(asked, lane)
		return lane != "lane-1"
	}
	exec, deps, _, rec, calls := retryRig(t, DefaultRetryPolicy(), eligible, map[string]func(int) error{"lane-1": transient503(0)})
	if _, err := exec.Execute(context.Background(), validReq()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls["lane-1"] != 1 || calls["lane-2"] != 1 || deps.router.calls != 2 || len(rec.delays) != 0 {
		t.Fatalf("lane-1 %d, lane-2 %d, Select %d, pauses %v; want 1, 1, 2 and no pause", calls["lane-1"], calls["lane-2"], deps.router.calls, rec.delays)
	}
	if len(asked) == 0 || asked[0] != "lane-1" {
		t.Fatalf("eligibility asked for %v, want lane-1 before its second attempt", asked)
	}
}
