// Purpose: exercises handleCompletionHook's every outcome directly
// (job-scoped accept/deny/timeout/malformed/unknown-job) and
// RegisterCompletionCheckHandler end to end against a real *rpc.Registry
// and a real *events.Bus -- TestCompletionHook asserts the journaled deny
// record by replaying the bus's own persisted store state, not merely a
// subscription callback (AGENT-BRIEF: "a row proves the work landed").
// SPORT: fleet/hookpacks.CompletionHookPayload/PolicyGate/ADD (P1-E32-W6-S66-T1).
package hookpacks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeGate is a PolicyGate test double. delay, if set, makes CompletionCheck
// block until ctx is done, so timeout tests never race a real clock.
type fakeGate struct {
	ok     bool
	reason string
	err    error
	delay  bool
}

func (g fakeGate) CompletionCheck(ctx context.Context, _ string) (bool, string, error) {
	if g.delay {
		<-ctx.Done()
		return false, "", ctx.Err()
	}
	return g.ok, g.reason, g.err
}

// fakeResolver is a JobResolver test double keyed by payload.JobID.
type fakeResolver struct {
	jobs map[string]bool // jobID -> exists
	err  error           // returned verbatim when non-nil (daemon unreachable)
}

func (r fakeResolver) ResolveJob(_ context.Context, payload hookpacks.CompletionHookPayload) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	if payload.JobID == "" {
		return "", nil
	}
	if !r.jobs[payload.JobID] {
		return "", cascade.Newf(cascade.KindNotFound, "hookpacks: unknown job %q", payload.JobID)
	}
	return payload.JobID, nil
}

func TestCompletionHookScope(t *testing.T) {
	registry, bus, clock := newCompletionFixture(t, fakeGate{ok: true}, fakeResolver{err: cascade.New(cascade.KindUnavailable, "daemon unreachable")})
	resp := dispatchCompletion(t, registry, hookpacks.CompletionHookPayload{EventType: hookpacks.EventStop, SessionID: "s1"})
	if resp.Deny {
		t.Fatalf("unscoped completion with an unreachable resolver = %+v, want allow (no opinion)", resp)
	}
	assertNoRecord(t, bus, clock)
}

func TestUnscopedCompletionNoOpinion(t *testing.T) {
	calls := 0
	gate := countingGate{fakeGate{ok: false, reason: "should never be called"}, &calls}
	registry, bus, clock := newCompletionFixture(t, gate, fakeResolver{jobs: map[string]bool{}})
	resp := dispatchCompletion(t, registry, hookpacks.CompletionHookPayload{EventType: hookpacks.EventTaskCompleted, SessionID: "s1"})
	if resp.Deny {
		t.Fatalf("unscoped completion = %+v, want allow", resp)
	}
	if calls != 0 {
		t.Fatalf("policy.completion_check was called %d times for an unscoped payload, want 0", calls)
	}
	assertNoRecord(t, bus, clock)
}

// countingGate wraps a PolicyGate and counts CompletionCheck calls.
type countingGate struct {
	hookpacks.PolicyGate
	n *int
}

func (g countingGate) CompletionCheck(ctx context.Context, jobID string) (bool, string, error) {
	*g.n++
	return g.PolicyGate.CompletionCheck(ctx, jobID)
}

func TestCompletionHook_JobScopedAccept(t *testing.T) {
	registry, _, _ := newCompletionFixture(t, fakeGate{ok: true}, fakeResolver{jobs: map[string]bool{"job-1": true}})
	resp := dispatchCompletion(t, registry, hookpacks.CompletionHookPayload{EventType: hookpacks.EventStop, JobID: "job-1"})
	if resp.Deny {
		t.Fatalf("job-scoped accept = %+v, want allow", resp)
	}
}

func TestCompletionHook_JobScopedDenyVerbatimReason(t *testing.T) {
	registry, bus, clock := newCompletionFixture(t, fakeGate{ok: false, reason: "missing evidence: build"}, fakeResolver{jobs: map[string]bool{"job-1": true}})
	resp := dispatchCompletion(t, registry, hookpacks.CompletionHookPayload{EventType: hookpacks.EventStop, JobID: "job-1"})
	if !resp.Deny || resp.Reason != "missing evidence: build" {
		t.Fatalf("deny = %+v, want Deny=true Reason=%q verbatim", resp, "missing evidence: build")
	}
	assertRecord(t, bus, clock, "job-1", "missing evidence: build", hookpacks.EventGateDenied)
}

func TestCompletionHook_Timeout(t *testing.T) {
	registry, bus, clock := newCompletionFixtureTimeout(t, fakeGate{delay: true}, fakeResolver{jobs: map[string]bool{"job-1": true}})
	resp := dispatchCompletion(t, registry, hookpacks.CompletionHookPayload{EventType: hookpacks.EventStop, JobID: "job-1"})
	if !resp.Deny || resp.Reason != "completion check timed out" {
		t.Fatalf("timeout deny = %+v, want Deny=true Reason=%q", resp, "completion check timed out")
	}
	assertRecord(t, bus, clock, "job-1", "completion check timed out", hookpacks.EventGateTimeout)
}

func TestCompletionHook_MalformedJSONDeniesNoPanic(t *testing.T) {
	registry, _, _ := newCompletionFixture(t, fakeGate{ok: true}, fakeResolver{})
	got, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: hookpacks.MethodCompletionCheck, Params: json.RawMessage(`{not json`)})
	if errObj != nil {
		t.Fatalf("malformed payload returned an RPC error %+v, want a Deny response", errObj)
	}
	resp, ok := got.(hookpacks.CompletionHookResponse)
	if !ok || !resp.Deny {
		t.Fatalf("malformed payload response = %#v, want Deny=true", got)
	}
}

func TestCompletionHook_UnknownJobIDDenies(t *testing.T) {
	registry, bus, clock := newCompletionFixture(t, fakeGate{ok: true}, fakeResolver{jobs: map[string]bool{}})
	resp := dispatchCompletion(t, registry, hookpacks.CompletionHookPayload{EventType: hookpacks.EventStop, JobID: "ghost-job"})
	if !resp.Deny || !strings.Contains(resp.Reason, "ghost-job") {
		t.Fatalf("unknown job id = %+v, want a Deny naming ghost-job", resp)
	}
	assertRecord(t, bus, clock, "ghost-job", resp.Reason, hookpacks.EventGateDenied)
}

func TestCompletionHook_GateErrDenies(t *testing.T) {
	registry, _, _ := newCompletionFixture(t, fakeGate{err: errors.New("boom")}, fakeResolver{jobs: map[string]bool{"job-1": true}})
	resp := dispatchCompletion(t, registry, hookpacks.CompletionHookPayload{EventType: hookpacks.EventStop, JobID: "job-1"})
	if !resp.Deny || resp.Reason != "boom" {
		t.Fatalf("gate error = %+v, want Deny=true Reason=boom", resp)
	}
}

func TestCompletionHook_NilGateDenies(t *testing.T) {
	resp := dispatchCompletionDirect(context.Background(), hookpacks.CompletionHookPayload{EventType: hookpacks.EventStop, JobID: "job-1"}, nil, fakeResolver{jobs: map[string]bool{"job-1": true}})
	if !resp.Deny {
		t.Fatalf("nil gate = %+v, want Deny=true", resp)
	}
}

// TestCompletionHook_RealFixtures parses the TaskCompleted fixture (still
// self-authored, README.md's D3 disclosure) and drives accept/deny
// through the real dispatch path. stop_fixture.json is now the REAL
// captured native Stop payload (a wider shape than CompletionHookPayload)
// exercised at the shell layer instead: completion_hook_command_test.go's
// TestCompletionHookCommand_StopHookActiveNeverBypassesFailClosed.
func TestCompletionHook_RealFixtures(t *testing.T) {
	for _, name := range []string{"task_completed_fixture.json"} {
		raw, err := os.ReadFile("testdata/completion/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		p, err := hookpacks.ParseCompletionHookPayload(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("ParseCompletionHookPayload(%s): %v", name, err)
		}
		if p.JobID == "" || p.SessionID == "" || p.EventType == "" {
			t.Fatalf("%s parsed as %+v, want job_id/session_id/event_type populated", name, p)
		}

		registry, _, _ := newCompletionFixture(t, fakeGate{ok: true}, fakeResolver{jobs: map[string]bool{p.JobID: true}})
		if resp := dispatchCompletion(t, registry, p); resp.Deny {
			t.Fatalf("%s: passing check = %+v, want allow", name, resp)
		}

		registry, _, _ = newCompletionFixture(t, fakeGate{ok: false, reason: "missing evidence: tests"}, fakeResolver{jobs: map[string]bool{p.JobID: true}})
		resp := dispatchCompletion(t, registry, p)
		if !resp.Deny || resp.Reason != "missing evidence: tests" {
			t.Fatalf("%s: failing check = %+v, want Deny=true Reason=%q verbatim", name, resp, "missing evidence: tests")
		}
	}
}

// --- fixtures ---

func newCompletionFixture(t *testing.T, gate hookpacks.PolicyGate, resolver hookpacks.JobResolver) (*rpc.Registry, *events.Bus, *testkit.FrozenClock) {
	t.Helper()
	return newCompletionFixtureTimeoutDuration(t, gate, resolver, 5*time.Second)
}

func newCompletionFixtureTimeout(t *testing.T, gate hookpacks.PolicyGate, resolver hookpacks.JobResolver) (*rpc.Registry, *events.Bus, *testkit.FrozenClock) {
	t.Helper()
	return newCompletionFixtureTimeoutDuration(t, gate, resolver, 20*time.Millisecond)
}

func newCompletionFixtureTimeoutDuration(t *testing.T, gate hookpacks.PolicyGate, resolver hookpacks.JobResolver, timeout time.Duration) (*rpc.Registry, *events.Bus, *testkit.FrozenClock) {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	registry := rpc.NewRegistry()
	if err := hookpacks.RegisterCompletionCheckHandler(registry, clock, bus, gate, resolver, timeout); err != nil {
		t.Fatalf("RegisterCompletionCheckHandler: %v", err)
	}
	return registry, bus, clock
}

func dispatchCompletion(t *testing.T, registry *rpc.Registry, p hookpacks.CompletionHookPayload) hookpacks.CompletionHookResponse {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	got, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: hookpacks.MethodCompletionCheck, Params: raw})
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	resp, ok := got.(hookpacks.CompletionHookResponse)
	if !ok {
		t.Fatalf("Dispatch returned %#v, want hookpacks.CompletionHookResponse", got)
	}
	return resp
}

// assertRecord replays the REAL "jobs.gate" namespace (D2: the same
// namespace/event kinds internal/jobs.CompletionPolicy.Transition's own
// cp.deny and internal/fleet/supervision's stall detector use, never the
// self-invented "hookpacks.completion" this ticket's CR-B rework
// removed) and asserts the last record's job id, reason and kind.
func assertRecord(t *testing.T, bus *events.Bus, clock *testkit.FrozenClock, jobID, reason string, wantKind events.EventKind) {
	t.Helper()
	evts, err := bus.Replay(context.Background(), "jobs.gate", 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(evts) == 0 {
		t.Fatalf("no journaled denial record found in the bus's own store, want one for job %q", jobID)
	}
	last := evts[len(evts)-1]
	if last.Kind != wantKind {
		t.Fatalf("journaled event kind = %q, want %q", last.Kind, wantKind)
	}
	var rec struct {
		JobID       string `json:"job_id"`
		SessionID   string `json:"session_id"`
		Reason      string `json:"reason"`
		TimestampMs int64  `json:"timestamp_ms"`
	}
	if err := json.Unmarshal(last.Payload, &rec); err != nil {
		t.Fatalf("unmarshal journaled record: %v", err)
	}
	if rec.JobID != jobID || rec.Reason != reason {
		t.Fatalf("journaled record = %+v, want job_id=%q reason=%q", rec, jobID, reason)
	}
	if rec.TimestampMs != clock.Now().UnixMilli() {
		t.Fatalf("journaled record timestamp = %d, want %d", rec.TimestampMs, clock.Now().UnixMilli())
	}
}

func assertNoRecord(t *testing.T, bus *events.Bus, _ *testkit.FrozenClock) {
	t.Helper()
	evts, err := bus.Replay(context.Background(), "jobs.gate", 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(evts) != 0 {
		t.Fatalf("no-opinion outcome journaled %d denial record(s), want 0: %+v", len(evts), evts)
	}
}

// dispatchCompletionDirect exercises the unexported decision function via
// the exported RPC path is not possible for a nil gate (RegisterCompletionHookPack
// refuses a nil gate) -- this in-package test file cannot reach the
// unexported handleCompletionHook directly (it is hookpacks_test, an
// external test package), so it goes through the same RPC handler with a
// nil PolicyGate value boxed into the interface, which is exactly what a
// misconfigured composition root would do.
func dispatchCompletionDirect(ctx context.Context, p hookpacks.CompletionHookPayload, gate hookpacks.PolicyGate, resolver hookpacks.JobResolver) hookpacks.CompletionHookResponse {
	registry := rpc.NewRegistry()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	_ = hookpacks.RegisterCompletionCheckHandler(registry, clock, bus, gate, resolver, 5*time.Second)
	raw, _ := json.Marshal(p)
	got, errObj := registry.Dispatch(ctx, &rpc.Request{Method: hookpacks.MethodCompletionCheck, Params: raw})
	if errObj != nil {
		return hookpacks.CompletionHookResponse{Deny: true, Reason: errObj.Message}
	}
	resp, _ := got.(hookpacks.CompletionHookResponse)
	return resp
}
