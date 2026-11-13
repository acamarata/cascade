//go:build !windows

package main

// Purpose: tests of the inbox delivery subscriber lifecycle (P1-HRN-07):
// live sessions are subscribed at start, registrations through the real
// fleet.sessions.hook_event handler subscribe, a SessionEnd unsubscribes,
// and an addressed item lands only in its own session's hydration queue.
// Constraints: in-memory store and bus, t.TempDir() HOME, one fixed clock.

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/claude"
)

// deliveryFixture is one in-memory daemon slice: store, bus, sessions
// store, the one notify.Service and the started subscriber.
type deliveryFixture struct {
	store provider.Store
	clock hydrationTestClock
	bus   *events.Bus
	live  *sessions.Store
	svc   *notify.Service
	subs  *sessionSubscriber
}

// newDeliveryFixture seeds recs into the sessions store, then starts the
// subscriber lifecycle over a test-built wiring.
func newDeliveryFixture(t *testing.T, recs ...sessions.SessionRecord) *deliveryFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("CASCADE_HOME", t.TempDir())
	f := &deliveryFixture{store: storetest.NewMemStore(), clock: hydrationTestClock{now: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}}
	f.bus = events.New(storetest.NewMemStore(), f.clock)
	f.live = sessions.New(f.store, f.clock, f.bus)
	for _, rec := range recs {
		if err := f.live.Upsert(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	f.svc = notify.NewService(notify.DefaultConfig(), f.clock, nil)
	ctx, cancel := context.WithCancel(context.Background())
	manifest := daemon.NewManifest(nil, f.clock)
	t.Cleanup(func() { cancel(); manifest.Wait() })
	w := &daemonWiring{Ctx: ctx, Store: f.store, Clock: f.clock, Bus: f.bus, Manifest: manifest,
		Deps: daemonRuntime{Notify: daemon.NewNotifyRuntime(f.svc)}}
	subs, err := startInboxDelivery(w)
	if err != nil {
		t.Fatalf("startInboxDelivery: %v", err)
	}
	f.subs = subs
	return f
}

// waitSubscribed polls Subscribed() until it equals want.
func waitSubscribed(t *testing.T, subs *sessionSubscriber, want []string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := subs.Subscribed()
		if reflect.DeepEqual(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Subscribed() = %v, want %v", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// registeredIDs returns the session ids the registry holds subscribers for.
func registeredIDs(reg *notify.Registry) map[string]bool {
	out := map[string]bool{}
	for _, r := range reg.Snapshot() {
		out[r.Session.SessionID] = true
	}
	return out
}

// sendHookEvent posts one hook payload through the real hook-event handler.
func sendHookEvent(t *testing.T, registry *rpc.Registry, p hookpacks.HookPayload) {
	t.Helper()
	raw, _ := json.Marshal(p)
	if _, errObj := registry.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: hookpacks.MethodHookEvent, ID: []byte("1"), Params: raw}); errObj != nil {
		t.Fatalf("%s hook event: %+v", p.EventType, errObj)
	}
}

func sessionRec(id, state string) sessions.SessionRecord {
	return sessions.SessionRecord{SessionID: id, Harness: "cc-harness", PID: 1, State: state}
}

func TestInboxDeliverySubscribesLiveSessions(t *testing.T) {
	f := newDeliveryFixture(t,
		sessionRec("sess-a", sessions.StateActive.String()),
		sessionRec("sess-b", sessions.StateIdle.String()),
		sessionRec("sess-closed", sessions.StateClosed.String()))
	waitSubscribed(t, f.subs, []string{"sess-a", "sess-b"})
	if ids := registeredIDs(f.svc.Registry); len(ids) != 2 || !ids["sess-a"] || !ids["sess-b"] {
		t.Fatalf("notify registry subscribers = %v, want sess-a and sess-b", ids)
	}

	registry := rpc.NewRegistry()
	if err := hookpacks.RegisterHookEventHandler(registry, f.live, nil, f.clock); err != nil {
		t.Fatal(err)
	}
	now := f.clock.now.UnixMilli()
	sendHookEvent(t, registry, hookpacks.HookPayload{Harness: "cc-harness", EventType: hookpacks.EventSessionStart, SessionID: "sess-new", PID: 2, TimestampMs: now})
	waitSubscribed(t, f.subs, []string{"sess-a", "sess-b", "sess-new"})

	sendHookEvent(t, registry, hookpacks.HookPayload{Harness: "cc-harness", EventType: hookpacks.EventSessionEnd, SessionID: "sess-new", PID: 2, TimestampMs: now})
	waitSubscribed(t, f.subs, []string{"sess-a", "sess-b"})
	if ids := registeredIDs(f.svc.Registry); ids["sess-new"] || ids["sess-closed"] {
		t.Fatalf("a closed session is still subscribed in the notify registry: %v", ids)
	}

	// A closed session republished on the bus stays unsubscribed.
	if err := f.live.Upsert(context.Background(), sessionRec("sess-closed", sessions.StateClosed.String())); err != nil {
		t.Fatal(err)
	}
	sendHookEvent(t, registry, hookpacks.HookPayload{Harness: "cc-harness", EventType: hookpacks.EventSessionStart, SessionID: "sess-last", PID: 3, TimestampMs: now})
	waitSubscribed(t, f.subs, []string{"sess-a", "sess-b", "sess-last"})
}

// hydrationQueues renders the hydration namespace of store, one key per entry.
func hydrationQueues(t *testing.T, store provider.Store) map[string]string {
	t.Helper()
	ctx := context.Background()
	it, err := store.Scan(ctx, claude.HydrationNamespace, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	out := map[string]string{}
	for it.Next(ctx) {
		out[it.Key()] = string(it.Value())
	}
	if err := it.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// queueFor returns the stored bytes of every hydration key naming sid.
func queueFor(queues map[string]string, sid string) string {
	var b strings.Builder
	for k, v := range queues {
		if strings.Contains(k, sid) {
			b.WriteString(k + "=" + v + "\n")
		}
	}
	return b.String()
}

func TestAddressedItemReachesOnlyItsSession(t *testing.T) {
	f := newDeliveryFixture(t,
		sessionRec("sess-a", sessions.StateActive.String()),
		sessionRec("sess-b", sessions.StateActive.String()))
	waitSubscribed(t, f.subs, []string{"sess-a", "sess-b"})
	before := hydrationQueues(t, f.store)

	ctx := context.Background()
	if err := f.svc.Notifier.Deliver(ctx, notify.Notification{
		ID: "addr-a-1", Class: notify.ClassAddressed, TargetSession: "sess-a", OriginScope: "repo-a",
		Visibility: notify.VisibilityPrivate, Payload: []byte("A-ONLY-BODY"),
		Origin: notify.Origin{Source: "test.producer", RecordRef: "addr-a-1", Tier: provider.SensitivityInternal},
	}); err != nil {
		t.Fatal(err)
	}
	f.svc.Dispatcher.Drain(ctx)
	after := hydrationQueues(t, f.store)

	if got := queueFor(after, "sess-a"); !strings.Contains(got, "addr-a-1") {
		t.Fatalf("session A's queue lacks the addressed item:\n%s", got)
	}
	if b0, b1 := queueFor(before, "sess-b"), queueFor(after, "sess-b"); b0 != b1 {
		t.Fatalf("session B's queue changed:\nbefore %q\nafter  %q", b0, b1)
	}
	for k, v := range after {
		if !strings.Contains(k, "sess-a") && strings.Contains(v, "addr-a-1") {
			t.Fatalf("the item addressed to A landed under %q", k)
		}
	}
}
