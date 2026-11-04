package hookpacks_test

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/pkg/cascade"
)

type fakeTaintStore struct {
	taints   map[string]sessions.TaintState
	marks    map[string]string
	taintErr error
	markErr  error
}

func newFakeStore() *fakeTaintStore {
	return &fakeTaintStore{
		taints: make(map[string]sessions.TaintState),
		marks:  make(map[string]string),
	}
}

func (f *fakeTaintStore) Taint(_ context.Context, id string) (sessions.TaintState, error) {
	if f.taintErr != nil {
		return sessions.TaintState{Tainted: true}, f.taintErr
	}
	ts, ok := f.taints[id]
	if !ok {
		return sessions.TaintState{}, cascade.New(cascade.KindNotFound, "not found")
	}
	return ts, nil
}

func (f *fakeTaintStore) MarkUntrusted(_ context.Context, id, ref string) error {
	if f.markErr != nil {
		return f.markErr
	}
	if _, ok := f.taints[id]; !ok {
		return cascade.New(cascade.KindNotFound, "not found")
	}
	f.marks[id] = ref
	f.taints[id] = sessions.TaintState{Tainted: true, ProvenanceRef: ref}
	return nil
}

type fakePolicy struct {
	allowed bool
	err     error
	calls   int
}

func (p *fakePolicy) AllowTool(_ context.Context, _, _ string) (bool, error) {
	p.calls++
	return p.allowed, p.err
}

func TestUntaintedSessionAllowed(t *testing.T) {
	store := newFakeStore()
	store.taints["s1"] = sessions.TaintState{Tainted: false}
	pol := &fakePolicy{}
	params := hookpacks.ToolGateParams{SessionID: "s1", ToolName: "Bash", HookEventName: "PreToolUse"}
	res, err := hookpacks.DecideToolGate(context.Background(), params, "claude-code", store, pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allow {
		t.Fatalf("expected allow for untainted session, got deny: %s", res.Reason)
	}
	if pol.calls != 0 {
		t.Fatalf("policy should not be called for untainted session, got %d calls", pol.calls)
	}
}

func TestTaintedTurnReadOnlyToolAllowed(t *testing.T) {
	store := newFakeStore()
	store.taints["s1"] = sessions.TaintState{Tainted: true}
	pol := &fakePolicy{}
	for _, tool := range []string{"Read", "Glob", "Grep", "LS", "NotebookRead"} {
		params := hookpacks.ToolGateParams{SessionID: "s1", ToolName: tool, HookEventName: "PreToolUse"}
		res, err := hookpacks.DecideToolGate(context.Background(), params, "claude-code", store, pol)
		if err != nil {
			t.Fatalf("tool %s: unexpected error: %v", tool, err)
		}
		if !res.Allow {
			t.Fatalf("tool %s: expected allow for read-only in tainted session, got deny: %s", tool, res.Reason)
		}
	}
	if pol.calls != 0 {
		t.Fatalf("policy should not be called for read-only tools, got %d calls", pol.calls)
	}
}

func TestTaintedTurnMutatingToolRefused(t *testing.T) {
	store := newFakeStore()
	store.taints["s1"] = sessions.TaintState{Tainted: true}
	mutating := []string{"Bash", "Write", "Edit", "WebFetch", "WebSearch", "UnknownTool"}
	for _, tool := range mutating {
		pol := &fakePolicy{allowed: false}
		params := hookpacks.ToolGateParams{SessionID: "s1", ToolName: tool, HookEventName: "PreToolUse"}
		res, err := hookpacks.DecideToolGate(context.Background(), params, "claude-code", store, pol)
		if err != nil {
			t.Fatalf("tool %s: unexpected error: %v", tool, err)
		}
		if res.Allow {
			t.Fatalf("tool %s: expected deny when policy refuses", tool)
		}
		if pol.calls != 1 {
			t.Fatalf("tool %s: expected 1 policy call, got %d", tool, pol.calls)
		}
	}
	// When policy allows, mutating tools pass.
	pol := &fakePolicy{allowed: true}
	params := hookpacks.ToolGateParams{SessionID: "s1", ToolName: "Bash", HookEventName: "PreToolUse"}
	res, err := hookpacks.DecideToolGate(context.Background(), params, "claude-code", store, pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allow {
		t.Fatalf("expected allow when policy permits, got deny: %s", res.Reason)
	}
}

func TestToolGateEngineErrorDenies(t *testing.T) {
	store := newFakeStore()
	store.taints["s1"] = sessions.TaintState{Tainted: true}
	pol := &fakePolicy{err: cascade.New(cascade.KindInternal, "engine down")}
	params := hookpacks.ToolGateParams{SessionID: "s1", ToolName: "Bash", HookEventName: "PreToolUse"}
	res, err := hookpacks.DecideToolGate(context.Background(), params, "claude-code", store, pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allow {
		t.Fatal("expected deny on engine error")
	}
}

func TestToolGateUnknownSessionDenies(t *testing.T) {
	store := newFakeStore()
	pol := &fakePolicy{}
	params := hookpacks.ToolGateParams{SessionID: "unknown-sess", ToolName: "Bash", HookEventName: "PreToolUse"}
	res, err := hookpacks.DecideToolGate(context.Background(), params, "claude-code", store, pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allow {
		t.Fatal("expected deny for unknown session")
	}
}

func TestToolGateHarnessMismatchDenies(t *testing.T) {
	store := newFakeStore()
	store.taints["s1"] = sessions.TaintState{Tainted: false}
	pol := &fakePolicy{}
	params := hookpacks.ToolGateParams{SessionID: "s1", ToolName: "Bash", HookEventName: "PreToolUse"}
	res, err := hookpacks.DecideToolGate(context.Background(), params, "other-harness", store, pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allow {
		t.Fatal("expected deny for harness mismatch")
	}
}

func TestToolGateMalformedInputDenies(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{"empty", []byte{}},
		{"non-json", []byte("not json at all")},
		{"too-large", []byte(strings.Repeat("x", (1<<20)+1))},
		{"wrong-event", []byte(`{"session_id":"s","tool_name":"Bash","hook_event_name":"Stop"}`)},
		{"empty-ids", []byte(`{"session_id":"","tool_name":"","hook_event_name":"PreToolUse"}`)},
	}
	store := newFakeStore()
	pol := &fakePolicy{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := hookpacks.ParseToolGateParams(tt.raw)
			if err != nil {
				return // parse error means deny
			}
			res, _ := hookpacks.DecideToolGate(context.Background(), params, "claude-code", store, pol)
			if res.Allow {
				t.Fatal("expected deny for malformed input")
			}
		})
	}
}

func TestToolGateStoreErrorDenies(t *testing.T) {
	store := newFakeStore()
	store.taints["s1"] = sessions.TaintState{Tainted: false}
	store.taintErr = cascade.New(cascade.KindInternal, "db down")
	pol := &fakePolicy{}
	params := hookpacks.ToolGateParams{SessionID: "s1", ToolName: "Bash", HookEventName: "PreToolUse"}
	res, err := hookpacks.DecideToolGate(context.Background(), params, "claude-code", store, pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allow {
		t.Fatal("expected deny on store error")
	}
}

func TestToolGateIgnoresNestedSessionID(t *testing.T) {
	store := newFakeStore()
	store.taints["outer-session"] = sessions.TaintState{Tainted: false}
	store.taints["inner-session"] = sessions.TaintState{Tainted: true}
	pol := &fakePolicy{}
	raw := []byte(`{"session_id":"outer-session","tool_name":"Bash","hook_event_name":"PreToolUse",` +
		`"tool_input":{"session_id":"inner-session"}}`)
	params, err := hookpacks.ParseToolGateParams(raw)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if params.SessionID != "outer-session" {
		t.Fatalf("expected top-level session_id, got %q", params.SessionID)
	}
	res, _ := hookpacks.DecideToolGate(context.Background(), params, "claude-code", store, pol)
	if !res.Allow {
		t.Fatal("expected allow using top-level session_id")
	}
}

func TestInboxMCPReadTaintsSession(t *testing.T) {
	store := newFakeStore()
	store.taints["s1"] = sessions.TaintState{Tainted: false}
	pol := &fakePolicy{}
	ctx := context.Background()

	// cascade_inbox_read on untainted session: marks and allows.
	params := hookpacks.ToolGateParams{SessionID: "s1", ToolName: "cascade_inbox_read", HookEventName: "PreToolUse"}
	res, err := hookpacks.DecideToolGate(ctx, params, "claude-code", store, pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Allow {
		t.Fatalf("expected allow for inbox read, got deny: %s", res.Reason)
	}
	if store.marks["s1"] != "mcp:+cascade_inbox_read" {
		t.Fatalf("expected mark with mcp:+cascade_inbox_read, got %q", store.marks["s1"])
	}

	// Next Bash call should be denied because session is now tainted.
	params2 := hookpacks.ToolGateParams{SessionID: "s1", ToolName: "Bash", HookEventName: "PreToolUse"}
	res2, err := hookpacks.DecideToolGate(ctx, params2, "claude-code", store, pol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.Allow {
		t.Fatal("expected deny for Bash after taint")
	}
	checkInboxTaintErrorPaths(t, pol)
}

// checkInboxTaintErrorPaths covers the user-scope MCP spelling, a mark error
// and an unknown session for inbox reads.
func checkInboxTaintErrorPaths(t *testing.T, pol *fakePolicy) {
	t.Helper()
	ctx := context.Background()

	// The plugin and user-scope MCP spellings taint; another MCP read does not.
	for _, name := range []string{"mcp__plugin_cascade_cascade__cascade_inbox_read", "mcp__cascade__cascade_inbox_read", "mcp__mysvc_read"} {
		store2 := newFakeStore()
		store2.taints["s2"] = sessions.TaintState{Tainted: false}
		params3 := hookpacks.ToolGateParams{SessionID: "s2", ToolName: name, HookEventName: "PreToolUse"}
		res3, err := hookpacks.DecideToolGate(ctx, params3, "claude-code", store2, pol)
		want := map[bool]string{true: "mcp:+" + name, false: ""}[name != "mcp__mysvc_read"]
		if err != nil || !res3.Allow || store2.marks["s2"] != want {
			t.Fatalf("%s: allow=%v err=%v mark=%q, want allow and mark %q", name, res3.Allow, err, store2.marks["s2"], want)
		}
	}

	// Mark error denies the inbox call.
	store3 := newFakeStore()
	store3.taints["s3"] = sessions.TaintState{Tainted: false}
	store3.markErr = cascade.New(cascade.KindInternal, "mark failed")
	params4 := hookpacks.ToolGateParams{SessionID: "s3", ToolName: "cascade_inbox_read", HookEventName: "PreToolUse"}
	res4, _ := hookpacks.DecideToolGate(ctx, params4, "claude-code", store3, pol)
	if res4.Allow {
		t.Fatal("expected deny when mark errors")
	}

	// Unknown session denies the inbox call.
	store4 := newFakeStore()
	params5 := hookpacks.ToolGateParams{SessionID: "unknown", ToolName: "cascade_inbox_read", HookEventName: "PreToolUse"}
	res5, _ := hookpacks.DecideToolGate(ctx, params5, "claude-code", store4, pol)
	if res5.Allow {
		t.Fatal("expected deny for unknown session on inbox call")
	}
}
