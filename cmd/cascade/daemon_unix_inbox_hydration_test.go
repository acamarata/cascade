//go:build !windows

package main

// Purpose: TestHydrationHookDeliversAddressedEnvelope (P1-E37-W8-S73-T1,
// R-14.323/Z2-41) — the primary hydration path end to end through the
// real context.slice handler: the session is registered by a SessionStart
// hook event sent to the real fleet.sessions.hook_event handler (never a
// pre-seeded row), an addressed notification goes through the real notify
// router into the durable inbox.hydration queue, and the real
// UserPromptSubmit hook (runContextSliceHook, fed the captured payload)
// prints the envelope in an untrusted-source data block after the
// directives. The slice is served from the in-process registry through the
// hook's fetch seam; the socket variant lives in the integration lane.
// Constraints: t.TempDir() only, no socket in this file, one fixed clock.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/claude"
	"github.com/acamarata/cascade/providers/sqlite"
)

type hydrationTestClock struct{ now time.Time }

func (c hydrationTestClock) Now() time.Time { return c.now }

// inboxRegistry registers the session hook handler and binds context.slice
// with the inbox drain behind the live-session gate, as the production
// context registration binds it when the flag is on.
func inboxRegistry(t *testing.T, paths runtime.PathProvider, store provider.Store, clock runtime.Clock, bus *events.Bus) *rpc.Registry {
	t.Helper()
	registry := rpc.NewRegistry()
	registry.SetAccessPolicy(nil) // a socket request's owner marker becomes the caller the drain rule needs
	if err := hookpacks.RegisterHookEventHandler(registry, sessions.New(store, clock, nil), nil, clock); err != nil {
		t.Fatal(err)
	}
	if err := registerContextEngineHandlers(registry, paths, clock, bus); err != nil {
		t.Fatalf("registerContextEngineHandlers: %v", err)
	}
	src, err := newBridgeInboxSource(context.Background(), &daemonWiring{Store: store, Clock: clock, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.RegisterContextAssembleHandler(registry, paths, clock, daemon.WithInboxHydration(src)); err != nil {
		t.Fatalf("RegisterContextAssembleHandler: %v", err)
	}
	return registry
}

// registryFetch serves the hook's slice request from registry as the owner
// CLI, the identity the real client presents on the socket.
func registryFetch(registry *rpc.Registry) func(context.Context, contextScopeDeps, daemon.ContextAssembleParams) (daemon.ContextSliceResult, error) {
	return func(ctx context.Context, _ contextScopeDeps, params daemon.ContextAssembleParams) (daemon.ContextSliceResult, error) {
		raw, _ := json.Marshal(params)
		owner := principal.WithCaller(ctx, principal.Caller{OwnerCLI: true})
		res, errObj := registry.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: daemon.ContextSliceMethod, ID: []byte("1"), Params: raw})
		if errObj != nil {
			return daemon.ContextSliceResult{}, cascade.New(cascade.KindUnavailable, errObj.Message)
		}
		return res.(daemon.ContextSliceResult), nil
	}
}

// capturedPrompt returns the captured UserPromptSubmit payload with its cwd
// pointed at dir, and the captured session id.
func capturedPrompt(t *testing.T, dir string) ([]byte, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "cc-hook-fixtures", "userpromptsubmit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	payload["cwd"] = dir
	out, _ := json.Marshal(payload)
	return out, payload["session_id"].(string)
}

// deliverAddressed runs one addressed notification through the real router
// into the durable queue the daemon drains (a second bridge over the same
// store: the queue is the store, not a process).
func deliverAddressed(t *testing.T, store provider.Store, clock runtime.Clock, sid string) {
	t.Helper()
	ctx := context.Background()
	bridge, err := plugins.NewClaudeInboxBridge(ctx, store, clock, supervision.NewStore(store, clock, nil, supervision.NewSystemIDGenerator(), 0))
	if err != nil {
		t.Fatal(err)
	}
	svc := notify.NewService(notify.DefaultConfig(), clock, nil)
	unwire, err := plugins.WireClaudeInboxPush(ctx, bridge, svc.Registry, notify.CandidateSession{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	defer unwire()
	if err := svc.Notifier.Deliver(ctx, notify.Notification{
		ID: "hook-env-1", Class: notify.ClassAddressed, TargetSession: sid, OriginScope: "repo-a",
		Visibility: notify.VisibilityPrivate, Payload: []byte("HOOK-BODY-SENTINEL " + strings.Repeat("z", 40)),
	}); err != nil {
		t.Fatal(err)
	}
	svc.Dispatcher.Drain(ctx)
}

// runHook runs the real hook once and returns its additionalContext.
func runHook(t *testing.T, deps contextScopeDeps, stdin []byte) string {
	t.Helper()
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: false})
	cmd, out := hookCommandFor(ctx, t, stdin)
	if err := runContextSliceHook(cmd, deps); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if out.Len() == 0 {
		return ""
	}
	var inj hookInjection
	if err := json.Unmarshal(out.Bytes(), &inj); err != nil {
		t.Fatalf("hook output %q is not an injection object: %v", out.String(), err)
	}
	return inj.HookSpecificOutput.AdditionalContext
}

func TestHydrationHookDeliversAddressedEnvelope(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	clock := hydrationTestClock{now: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)}
	paths, store, bus := doctorTestPaths(t), storetest.NewMemStore(), events.New(storetest.NewMemStore(), clock)
	registry := inboxRegistry(t, paths, store, clock, bus)
	orig := fetchHookSlice
	fetchHookSlice = registryFetch(registry)
	t.Cleanup(func() { fetchHookSlice = orig })
	deps := contextScopeDeps{Paths: paths, Getenv: func(string) string { return "" }, Environ: func() []string { return nil },
		DialContext: client.UnixDialer}
	stdin, sid := capturedPrompt(t, t.TempDir())
	start := hookpacks.HookPayload{Harness: "cc-harness", EventType: hookpacks.EventSessionStart, SessionID: sid, PID: 1, TimestampMs: clock.now.UnixMilli()}
	raw, _ := json.Marshal(start)
	if _, errObj := registry.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: hookpacks.MethodHookEvent, ID: []byte("1"), Params: raw}); errObj != nil {
		t.Fatalf("SessionStart hook event: %+v", errObj)
	}
	deliverAddressed(t, store, clock, sid)
	unknown := strings.Replace(string(stdin), sid, "not-a-live-session", 1)
	if got := runHook(t, deps, []byte(unknown)); strings.Contains(got, "hook-env-1") {
		t.Fatalf("an unknown session received the envelope:\n%s", got)
	}
	got := runHook(t, deps, stdin)
	if !strings.Contains(got, `<cascade-inbox-data provenance="untrusted-source">`) || !strings.Contains(got, `- id="hook-env-1" class="addressed"`) ||
		!strings.HasSuffix(got, "</cascade-inbox-data>") || strings.Contains(got, "HOOK-BODY-SENTINEL "+strings.Repeat("z", 40)) {
		t.Fatalf("hook output lacks the untrusted data block or carries the body:\n%s", got)
	}
	if again := runHook(t, deps, stdin); strings.Contains(again, "hook-env-1") {
		t.Fatalf("the envelope was delivered twice:\n%s", again)
	}
}

// seedHydrationQueue writes one addressed envelope for sid into the
// hydration queue of the cascade.db under paths, then closes it.
func seedHydrationQueue(t *testing.T, paths runtime.PathProvider, clock runtime.Clock, sid string) {
	t.Helper()
	ctx := context.Background()
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	seed, err := sqlite.Open(ctx, filepath.Join(paths.DataDir(), "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := claude.NewInboxHydration(seed, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	ev := claude.InboxEvent{ID: "flag-off-1", Class: "addressed", TargetSession: sid, Visibility: "private", Body: []byte("QUEUED-BODY")}
	if err := h.Enqueue(ctx, sid, ev); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
}

// hydrationQueueBytes renders the hydration namespace of the cascade.db
// under paths (opened after the daemon stopped).
func hydrationQueueBytes(t *testing.T, paths runtime.PathProvider) string {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(paths.DataDir(), "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	it, err := db.Scan(ctx, claude.HydrationNamespace, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	var b strings.Builder
	for it.Next(ctx) {
		b.WriteString(it.Key() + "=" + string(it.Value()) + "\n")
	}
	if err := it.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestHydrationProductionFlagOff composes the real daemon: inbox.hydration
// is recorded Skipped, and a context.slice for a live session whose queue
// holds an item returns no Inbox while the queue stays byte-identical.
func TestHydrationProductionFlagOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	_, sid := capturedPrompt(t, t.TempDir())
	seedHydrationQueue(t, deps.Paths, deps.Clock, sid)
	before := hydrationQueueBytes(t, deps.Paths)
	if !strings.Contains(before, "flag-off-1") {
		t.Fatalf("the seeded queue is empty, the test would prove nothing:\n%s", before)
	}
	var registry *rpc.Registry
	opts, cleanups, err := composeDaemon(context.Background(), deps, func(r *rpc.Registry, _ *rpc.SSEMux) { registry = r })
	if err != nil {
		runCleanupsLIFO(cleanups)
		t.Fatalf("composeDaemon: %v", err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			runCleanupsLIFO(cleanups)
		}
	}
	t.Cleanup(stop)
	if s, ok := subsystemState(opts.Manifest, inboxHydrationSubsystem); !ok || s.State != daemon.SubsystemSkipped || !strings.Contains(s.Detail, "P1-HRN-08") {
		t.Fatalf("inbox.hydration manifest entry = (%+v, %v), want Skipped naming the tool gate", s, ok)
	}
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	start := hookpacks.HookPayload{Harness: "cc-harness", EventType: hookpacks.EventSessionStart, SessionID: sid, PID: 1, TimestampMs: deps.Clock.Now().UnixMilli()}
	params, _ := json.Marshal(start)
	if _, errObj := registry.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: hookpacks.MethodHookEvent, ID: []byte("1"), Params: params}); errObj != nil {
		t.Fatalf("SessionStart: %+v", errObj)
	}
	cwd, _ := os.Getwd()
	params, _ = json.Marshal(daemon.ContextAssembleParams{Cwd: cwd, SessionID: sid})
	res, errObj := registry.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: daemon.ContextSliceMethod, ID: []byte("2"), Params: params})
	if errObj != nil {
		t.Fatalf("context.slice: %+v", errObj)
	}
	if got := res.(daemon.ContextSliceResult).Inbox; got != nil {
		t.Fatalf("flag-off context.slice returned Inbox %+v", got)
	}
	stop()
	if after := hydrationQueueBytes(t, deps.Paths); after != before {
		t.Fatalf("the flag-off slice changed the queue:\nbefore %s\nafter  %s", before, after)
	}
}
