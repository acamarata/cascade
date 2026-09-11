// Purpose: exercises RegisterHookEventHandler against a real *rpc.Registry
// and sessions.Store: every R-16.48 mapping-table row, validation
// refusals, the unknown-event-type drop, domain-error propagation, the
// planted-credential canary, and the Windows tier-2 refusal. Also
// FuzzHookPayload (HOW step 5): decode+validate must never panic.
// SPORT: fleet/hookpacks (ADD, per T-4 sport_updates).
package hookpacks_test

import (
	"context"
	"encoding/json"
	"errors"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recordingBus is a minimal sessions.EventBus fake that records every
// Publish call, so publishJob's real (non-nil-bus) branch and every
// jobs.*/fleet.sessions.* fan-out this handler emits can be asserted
// directly, without a live *events.Bus.
type recordingBus struct {
	calls []struct {
		namespace string
		kind      events.EventKind
	}
}

func (b *recordingBus) Publish(_ context.Context, namespace string, kind events.EventKind, _ string, _ []byte) (events.Event, error) {
	b.calls = append(b.calls, struct {
		namespace string
		kind      events.EventKind
	}{namespace, kind})
	return events.Event{}, nil
}

// newHandlerFixture builds a registry bound to a fresh store. bus may be nil.
func newHandlerFixture(t *testing.T, bus sessions.EventBus) (*rpc.Registry, *sessions.Store, *testkit.FrozenClock) {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	store := sessions.New(storetest.NewMemStore(), clock, nil)
	registry := rpc.NewRegistry()
	if err := hookpacks.RegisterHookEventHandler(registry, store, bus, clock); err != nil {
		if goruntime.GOOS == "windows" {
			t.Skip("RegisterHookEventHandler refuses on windows tier-2")
		}
		t.Fatalf("RegisterHookEventHandler: %v", err)
	}
	return registry, store, clock
}

func dispatchRaw(t *testing.T, registry *rpc.Registry, raw json.RawMessage) (any, *rpc.ErrorObject) {
	t.Helper()
	return registry.Dispatch(context.Background(), &rpc.Request{Method: hookpacks.MethodHookEvent, Params: raw})
}

func encodePayload(t *testing.T, p hookpacks.HookPayload) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return raw
}

// sendHook dispatches evt for sessionID and fails t on any RPC error.
func sendHook(t *testing.T, registry *rpc.Registry, sessionID string, evt hookpacks.HookEventType, ts int64) {
	t.Helper()
	p := hookpacks.HookPayload{Harness: "test-harness", EventType: evt, SessionID: sessionID, TimestampMs: ts}
	if _, errObj := dispatchRaw(t, registry, encodePayload(t, p)); errObj != nil {
		t.Fatalf("%s: %+v", evt, errObj)
	}
}

// TestHookRPCHandler_CoreDispatchFlow: the three fixture-backed types.
func TestHookRPCHandler_CoreDispatchFlow(t *testing.T) {
	registry, store, clock := newHandlerFixture(t, nil)
	now := clock.Now().UnixMilli()
	sendHook(t, registry, "s1", hookpacks.EventSessionStart, now)
	sendHook(t, registry, "s1", hookpacks.EventPreToolUse, now)

	rec, err := store.Get(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.State != sessions.StateActive.String() || rec.ToolCount != 1 || rec.LastToolAt == nil {
		t.Fatalf("record after start+tool-use = %+v, want state=active tool_count=1 last_tool_at set", rec)
	}

	sendHook(t, registry, "s1", hookpacks.EventStop, now)
	rec, err = store.Get(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Get after Stop: %v", err)
	}
	if rec.State != sessions.StateIdle.String() {
		t.Fatalf("state after Stop = %q, want %q", rec.State, sessions.StateIdle.String())
	}
}

// TestHookRPCHandler_ValidationRefusals table-drives every required-field
// / harness / timestamp refusal, plus the unknown-field refusal.
func TestHookRPCHandler_ValidationRefusals(t *testing.T) {
	registry, _, clock := newHandlerFixture(t, nil)
	now := clock.Now().UnixMilli()
	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"malformed_unknown_field", json.RawMessage(`{"harness":"test-harness","event_type":"Stop","session_id":"s4","surprise_field":"nope"}`)},
		{"unknown_harness", encodePayload(t, hookpacks.HookPayload{Harness: "unknown", EventType: hookpacks.EventStop, SessionID: "s5", TimestampMs: now})},
		{"empty_harness", encodePayload(t, hookpacks.HookPayload{Harness: "", EventType: hookpacks.EventStop, SessionID: "s5b", TimestampMs: now})},
		{"missing_session_id", encodePayload(t, hookpacks.HookPayload{Harness: "test-harness", EventType: hookpacks.EventStop, TimestampMs: now})},
		{"timestamp_out_of_range", encodePayload(t, hookpacks.HookPayload{Harness: "test-harness", EventType: hookpacks.EventStop, SessionID: "s6", TimestampMs: clock.Now().Add(-48 * time.Hour).UnixMilli()})},
		{"zero_timestamp", encodePayload(t, hookpacks.HookPayload{Harness: "test-harness", EventType: hookpacks.EventStop, SessionID: "s6b"})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, errObj := dispatchRaw(t, registry, c.raw)
			if errObj == nil {
				t.Fatalf("%s: want a refusal, got none", c.name)
			}
			if errObj.Code != cascade.KindInvalidInput.JSONRPCCode() {
				t.Fatalf("%s: code = %d, want invalid_input's JSON-RPC code %d", c.name, errObj.Code, cascade.KindInvalidInput.JSONRPCCode())
			}
		})
	}
}

func TestHookRPCHandler_UnknownEventTypeDroppedNotError(t *testing.T) {
	registry, _, clock := newHandlerFixture(t, nil)
	before := hookpacks.UnknownEventCount()
	p := hookpacks.HookPayload{Harness: "test-harness", EventType: hookpacks.HookEventType("SomeFutureEvent"), SessionID: "s7", TimestampMs: clock.Now().UnixMilli()}
	if _, errObj := dispatchRaw(t, registry, encodePayload(t, p)); errObj != nil {
		t.Fatalf("unknown event type must be dropped, not an error: %+v", errObj)
	}
	if hookpacks.UnknownEventCount() != before+1 {
		t.Fatalf("UnknownEventCount = %d, want %d", hookpacks.UnknownEventCount(), before+1)
	}
}

// TestHookRPCHandler_DomainErrorPropagates: Stop on a never-started
// session hits sessions.ErrNotFound, never a masked nil.
func TestHookRPCHandler_DomainErrorPropagates(t *testing.T) {
	registry, _, clock := newHandlerFixture(t, nil)
	p := hookpacks.HookPayload{Harness: "test-harness", EventType: hookpacks.EventStop, SessionID: "never-started", TimestampMs: clock.Now().UnixMilli()}
	if _, errObj := dispatchRaw(t, registry, encodePayload(t, p)); errObj == nil {
		t.Fatal("want ErrNotFound to propagate as a structured error")
	}
}

// TestHookRPCHandler_WindowsTier2Refusal mirrors sessions/rpc_test.go's
// Windows self-skip pattern: self-skip off-Windows, run for real on CI.
func TestHookRPCHandler_WindowsTier2Refusal(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("this refusal is GOOS-gated (handler.go); only Windows CI actually exercises it")
	}
	registry := rpc.NewRegistry()
	clock := testkit.NewFrozenClock(time.Now())
	store := sessions.New(storetest.NewMemStore(), clock, nil)
	err := hookpacks.RegisterHookEventHandler(registry, store, nil, clock)
	if !errors.Is(err, hookpacks.ErrWindowsTier2Unavailable) {
		t.Fatalf("RegisterHookEventHandler on windows tier-2 = %v, want ErrWindowsTier2Unavailable", err)
	}
	if registry.Registered(hookpacks.MethodHookEvent) {
		t.Fatal("windows tier-2 must never register the method")
	}
}

// TestHookRPCHandler_CredentialShapedFieldNeverDecodes is the planted-
// credential canary: a value via a field HookPayload does not declare
// must be refused outright, never silently dropped and accepted. The
// literal is split so no contiguous credential-shaped match exists here.
func TestHookRPCHandler_CredentialShapedFieldNeverDecodes(t *testing.T) {
	registry, _, _ := newHandlerFixture(t, nil)
	planted := "AKIA" + "7YQ2XPLM4RZV6WTB"
	raw := json.RawMessage(`{"harness":"test-harness","event_type":"Stop","session_id":"s8","leaked_secret":"` + planted + `"}`)
	if _, errObj := dispatchRaw(t, registry, raw); errObj == nil {
		t.Fatal("a payload carrying an undeclared field must be refused outright, never accepted")
	}
}

// TestHookRPCHandler_ActivityColumnsAndCompaction covers the activity
// columns and PreCompact/PostCompact (compaction_count).
func TestHookRPCHandler_ActivityColumnsAndCompaction(t *testing.T) {
	registry, store, clock := newHandlerFixture(t, &recordingBus{})
	now := clock.Now().UnixMilli()
	for _, evt := range []hookpacks.HookEventType{
		hookpacks.EventSessionStart, hookpacks.EventInstructionsLoaded, hookpacks.EventUserPromptSubmit,
		hookpacks.EventPostToolBatch, hookpacks.EventPreCompact, hookpacks.EventPostCompact,
	} {
		sendHook(t, registry, "s10", evt, now)
	}
	rec, err := store.Get(context.Background(), "s10")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.InstructionsLoadedAt == nil || rec.LastPromptAt == nil || rec.LastToolAt == nil {
		t.Fatalf("record = %+v, want all three activity timestamps set", rec)
	}
	if rec.ToolCount != 1 || rec.CompactionCount != 2 {
		t.Fatalf("record = %+v, want tool_count=1 compaction_count=2", rec)
	}
}

// TestHookRPCHandler_SubagentAndSessionEndTransitions: both transition to
// closed.
func TestHookRPCHandler_SubagentAndSessionEndTransitions(t *testing.T) {
	registry, store, clock := newHandlerFixture(t, &recordingBus{})
	now := clock.Now().UnixMilli()
	ctx := context.Background()

	sendHook(t, registry, "child-1", hookpacks.EventSubagentStart, now)
	sendHook(t, registry, "child-1", hookpacks.EventSubagentStop, now)
	child, err := store.Get(ctx, "child-1")
	if err != nil {
		t.Fatalf("Get child: %v", err)
	}
	if child.State != sessions.StateClosed.String() {
		t.Fatalf("child state = %q, want closed", child.State)
	}

	sendHook(t, registry, "s11", hookpacks.EventSessionStart, now)
	sendHook(t, registry, "s11", hookpacks.EventSessionEnd, now)
	rec, err := store.Get(ctx, "s11")
	if err != nil {
		t.Fatalf("Get after SessionEnd: %v", err)
	}
	if rec.State != sessions.StateClosed.String() {
		t.Fatalf("state after SessionEnd = %q, want closed", rec.State)
	}
}

// TestHookRPCHandler_TaskAndWorktreeFanOutThroughBus: each event fans out
// through publishJob's real, non-nil-bus branch.
func TestHookRPCHandler_TaskAndWorktreeFanOutThroughBus(t *testing.T) {
	bus := &recordingBus{}
	registry, _, clock := newHandlerFixture(t, bus)
	now := clock.Now().UnixMilli()
	sendHook(t, registry, "s12", hookpacks.EventSessionStart, now)
	for _, evt := range []hookpacks.HookEventType{
		hookpacks.EventTaskCreated, hookpacks.EventTaskCompleted,
		hookpacks.EventWorktreeCreate, hookpacks.EventWorktreeRemove,
	} {
		sendHook(t, registry, "s12", evt, now)
	}

	want := map[string]int{"jobs.task": 2, "jobs.worktree": 2}
	for _, c := range bus.calls {
		if _, ok := want[c.namespace]; ok {
			want[c.namespace]--
		}
	}
	for ns, remaining := range want {
		if remaining > 0 {
			t.Fatalf("namespace %q: %d expected publish(es) never observed (calls=%+v)", ns, remaining, bus.calls)
		}
	}
}

// FuzzHookPayload seeds from testdata/fuzz/FuzzHookPayload/seed_hook.json
// and asserts decode+validate+dispatch never panics on arbitrary input.
func FuzzHookPayload(f *testing.F) {
	seed, err := json.Marshal(hookpacks.HookPayload{
		Harness: "test-harness", EventType: hookpacks.EventPreToolUse,
		SessionID: "seed", PID: 1, Account: "a", TimestampMs: 1_700_000_000_000,
	})
	if err != nil {
		f.Fatalf("marshal seed: %v", err)
	}
	f.Add(seed)
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(``))

	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	store := sessions.New(storetest.NewMemStore(), clock, nil)
	registry := rpc.NewRegistry()
	if err := hookpacks.RegisterHookEventHandler(registry, store, nil, clock); err != nil {
		if goruntime.GOOS == "windows" {
			f.Skip("no registry to fuzz against on windows tier-2") // not a bare return: fuzz requires F.Fuzz/Fail/Skip
		}
		f.Fatalf("RegisterHookEventHandler: %v", err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("FuzzHookPayload panicked on %q: %v", data, r)
			}
		}()
		_, _ = dispatchRaw(t, registry, json.RawMessage(data))
	})
}
